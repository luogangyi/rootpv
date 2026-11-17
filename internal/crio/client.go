// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

// Package crio implements the bounded, local inspection protocol exposed by
// CRI-O. In particular, /containers also sees a container during OCI create,
// when CRI ContainerStatus deliberately still reports NotFound.
package crio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"time"

	"github.com/BurntSushi/toml"
)

const Provider = "cri-o"

var ErrNotFound = errors.New("CRI-O container not found")
var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidContainerID(id string) bool { return containerIDPattern.MatchString(id) }

type Client struct{ Socket string }

type Container struct {
	ImageRef        string            `json:"image_ref"`
	Root            string            `json:"root"`
	Sandbox         string            `json:"sandbox"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	CrioAnnotations map[string]string `json:"crio_annotations"`
}

func (c Client) Get(ctx context.Context, path string) ([]byte, error) {
	if !filepath.IsAbs(c.Socket) {
		return nil, fmt.Errorf("CRI-O socket must be an absolute path")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CRI-O %s returned HTTP %d", path, resp.StatusCode)
	}
	const limit = 8 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if len(data) > limit {
		return nil, fmt.Errorf("CRI-O response exceeds size limit")
	}
	return data, err
}

func (c Client) Container(ctx context.Context, id string) (Container, error) {
	if !ValidContainerID(id) {
		return Container{}, fmt.Errorf("invalid CRI-O container ID")
	}
	data, err := c.Get(ctx, "/containers/"+id)
	if err != nil {
		return Container{}, err
	}
	var container Container
	err = json.Unmarshal(data, &container)
	return container, err
}

// Config returns the effective configuration, including defaults and drop-ins.
func (c Client) Config(ctx context.Context) (map[string]any, error) {
	data, err := c.Get(ctx, "/config")
	if err != nil {
		return nil, err
	}
	var config map[string]any
	_, err = toml.Decode(string(data), &config)
	return config, err
}

// An image reference protects against normal GC, not destructive store repair.
// Require operators to disable automatic wipe before creating persistent roots.
func ValidateStorageSafety(raw map[string]any) error {
	root, ok := raw["crio"].(map[string]any)
	if !ok {
		return fmt.Errorf("CRI-O storage configuration unavailable")
	}
	for _, key := range []string{"internal_wipe", "internal_repair"} {
		if enabled, known := root[key].(bool); !known || enabled {
			return fmt.Errorf("CRI-O %s must be false before enabling RootPV", key)
		}
	}
	return nil
}

// StorageRequest is the versioned stdin protocol of rootpv-crio-storage. Its
// roots come from the local daemon, never from Pod annotations or OCI paths.
type StorageRequest struct {
	Version       int      `json:"version"`
	ContainerID   string   `json:"containerID"`
	GraphRoot     string   `json:"graphRoot"`
	RunRoot       string   `json:"runRoot"`
	ImageStore    string   `json:"imageStore,omitempty"`
	DriverOptions []string `json:"driverOptions,omitempty"`
}

type StorageSource struct {
	ImageID       string   `json:"imageID"`
	LayerID       string   `json:"layerID"`
	RootfsChainID string   `json:"rootfsChainID"`
	MountPoint    string   `json:"mountPoint"`
	LowerDirs     []string `json:"lowerDirs"`
	UpperDir      string   `json:"upperDir"`
	WorkDir       string   `json:"workDir"`
}
