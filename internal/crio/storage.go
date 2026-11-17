// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package crio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gitcode.com/openFuyao/rootpv/internal/atomicfile"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

type Storage struct {
	Client    Client
	Helper    string
	StateRoot string
}

type sourceRecord struct {
	Request StorageRequest              `json:"request"`
	Socket  string                      `json:"socket"`
	BootID  string                      `json:"bootID"`
	Owner   rootfsstate.ProcessIdentity `json:"owner"`
}

func (s Storage) Directory(id string) (string, error) {
	if !ValidContainerID(id) || !filepath.IsAbs(s.StateRoot) {
		return "", fmt.Errorf("invalid CRI-O source directory")
	}
	return filepath.Join(s.StateRoot, id), nil
}

// Acquire journals intent before creating a non-running storage reference.
// This reference protects image files even if CRI-O rolls back its container.
func (s Storage) Acquire(ctx context.Context, id string, owner rootfsstate.ProcessIdentity) (StorageSource, error) {
	dir, err := s.Directory(id)
	if err != nil {
		return StorageSource{}, err
	}
	raw, err := s.Client.Config(ctx)
	if err != nil {
		return StorageSource{}, err
	}
	req, err := storageRequest(raw, id)
	if err != nil {
		return StorageSource{}, err
	}
	if err := s.writeIntent(dir, req, owner); err != nil {
		return StorageSource{}, err
	}
	data, err := s.call(ctx, "acquire", req)
	if err != nil {
		return StorageSource{}, errors.Join(err, s.Release(context.WithoutCancel(ctx), id))
	}
	var source StorageSource
	if err := json.Unmarshal(data, &source); err != nil {
		return StorageSource{}, errors.Join(err, s.Release(context.WithoutCancel(ctx), id))
	}
	return source, nil
}

func (s Storage) writeIntent(dir string, req StorageRequest, owner rootfsstate.ProcessIdentity) error {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	if !owner.Valid() {
		return fmt.Errorf("source owner identity required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "record.json")); !os.IsNotExist(err) {
		return fmt.Errorf("CRI-O source intent already exists or is unreadable; recovery required")
	}
	record := sourceRecord{Request: req, Socket: s.Client.Socket, BootID: strings.TrimSpace(string(boot)), Owner: owner}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, "record.json.tmp"), filepath.Join(dir, "record.json"), data, 0o600)
}

func storageRequest(raw map[string]any, id string) (StorageRequest, error) {
	if err := ValidateStorageSafety(raw); err != nil {
		return StorageRequest{}, err
	}
	crio, _ := raw["crio"].(map[string]any)
	text := func(key string) string { value, _ := crio[key].(string); return value }
	if text("storage_driver") != "overlay" {
		return StorageRequest{}, fmt.Errorf("CRI-O overlay storage is required")
	}
	req := StorageRequest{Version: 1, ContainerID: id, GraphRoot: text("root"), RunRoot: text("runroot"), ImageStore: text("imagestore")}
	if req.ImageStore != "" {
		return StorageRequest{}, fmt.Errorf("separate CRI-O image stores are unsupported")
	}
	var err error
	req.DriverOptions, err = storageDriverOptions(crio["storage_option"])
	if err != nil {
		return StorageRequest{}, err
	}
	if !filepath.IsAbs(req.GraphRoot) || !filepath.IsAbs(req.RunRoot) {
		return StorageRequest{}, fmt.Errorf("CRI-O storage roots unavailable")
	}
	return req, nil
}

func storageDriverOptions(raw any) ([]string, error) {
	var result []string
	options, _ := raw.([]any)
	for _, option := range options {
		value, ok := option.(string)
		if !ok {
			continue
		}
		if strings.HasPrefix(value, "overlay.imagestore=") || strings.HasPrefix(value, "overlay.additionalimagestore") || strings.HasPrefix(value, "overlay.additionallayerstore") {
			return nil, fmt.Errorf("additional CRI-O image stores are unsupported")
		}
		result = append(result, value)
	}
	return result, nil
}

func (s Storage) call(ctx context.Context, operation string, req StorageRequest) ([]byte, error) {
	if !filepath.IsAbs(s.Helper) {
		return nil, fmt.Errorf("CRI-O storage helper must be an absolute path")
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	// Cleanup uses WithoutCancel so it can release a failed create's pin, but
	// must still bound a helper waiting for a containers/storage lock.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, s.Helper, "--operation", operation)
	command.Stdin = bytes.NewReader(data)
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("CRI-O storage %s: %w: %s", operation, err, strings.TrimSpace(diagnostic.String()))
	}
	return output, nil
}

func (s Storage) record(id string) (sourceRecord, error) {
	dir, err := s.Directory(id)
	if err != nil {
		return sourceRecord{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "record.json"))
	if err != nil {
		return sourceRecord{}, err
	}
	var record sourceRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	if record.Request.ContainerID != id || record.Request.Version != 1 || record.Socket != s.Client.Socket {
		return record, fmt.Errorf("CRI-O source identity or endpoint mismatch")
	}
	return record, nil
}

// Release is called only after mount/delegate cleanup. Retain intent on any
// failure so that restart recovery can retry without losing the image pin.
func (s Storage) Release(ctx context.Context, id string) error {
	record, err := s.record(id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = s.call(ctx, "release", record.Request); err != nil {
		return err
	}
	dir, err := s.Directory(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Reap only handles the pre-state crash window. Existing runtime states are
// owned by the normal lease reconciler. A failed daemon lookup retains pins.
func (s Storage) Reap(ctx context.Context, runtimeRoot string, alive func(rootfsstate.ProcessIdentity) bool) error {
	entries, err := os.ReadDir(s.StateRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	var result error
	for _, entry := range entries {
		if !entry.IsDir() || !ValidContainerID(entry.Name()) {
			continue
		}
		if err := s.reapOne(ctx, entry.Name(), runtimeRoot, strings.TrimSpace(string(boot)), alive); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s Storage) reapOne(ctx context.Context, id, runtimeRoot, boot string, alive func(rootfsstate.ProcessIdentity) bool) error {
	record, err := s.record(id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.BootID == boot && alive(record.Owner) {
		return nil
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, id)); !os.IsNotExist(err) {
		return err
	}
	active, err := s.Client.Active(ctx, id)
	if err != nil || active {
		return err
	}
	return s.Release(ctx, id)
}
