// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

// rootpv-crio-storage is intentionally a separate module: the storage format
// and Go toolchain track CRI-O without upgrading the containerd binaries.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	imagev1 "github.com/opencontainers/image-spec/specs-go/v1"
	storage "go.podman.io/storage"
	"go.podman.io/storage/pkg/reexec"
)

type request struct {
	Version       int      `json:"version"`
	ContainerID   string   `json:"containerID"`
	GraphRoot     string   `json:"graphRoot"`
	RunRoot       string   `json:"runRoot"`
	ImageStore    string   `json:"imageStore,omitempty"`
	DriverOptions []string `json:"driverOptions,omitempty"`
}

type source struct {
	ImageID       string   `json:"imageID"`
	LayerID       string   `json:"layerID"`
	RootfsChainID string   `json:"rootfsChainID"`
	MountPoint    string   `json:"mountPoint"`
	LowerDirs     []string `json:"lowerDirs"`
	UpperDir      string   `json:"upperDir"`
	WorkDir       string   `json:"workDir"`
}

func main() {
	if reexec.Init() {
		return
	}
	operation := flag.String("operation", "", "acquire or release one RootPV image reference")
	flag.Parse()
	if err := run(*operation); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(operation string) error {
	if operation != "acquire" && operation != "release" {
		return fmt.Errorf("unsupported operation")
	}
	req, err := readRequest(os.Stdin)
	if err != nil {
		return err
	}
	store, err := storage.GetStore(storage.StoreOptions{GraphRoot: req.GraphRoot, RunRoot: req.RunRoot, ImageStore: req.ImageStore, GraphDriverName: "overlay", GraphDriverOptions: referenceDriverOptions(req.DriverOptions)})
	if err != nil {
		return err
	}
	// Do not call Shutdown: it can unmount another process's storage layers.
	sum := sha256.Sum256([]byte("rootpv-crio-reference-v1\x00" + req.ContainerID))
	pinID := hex.EncodeToString(sum[:])
	metadata := "rootpv-crio-reference-v1:" + req.ContainerID
	if operation == "release" {
		return release(store, pinID, metadata)
	}
	result, err := acquire(store, req.ContainerID, pinID, metadata)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func readRequest(input io.Reader) (request, error) {
	var req request
	if err := json.NewDecoder(io.LimitReader(input, 64<<10)).Decode(&req); err != nil {
		return request{}, err
	}
	if req.Version != 1 || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(req.ContainerID) {
		return request{}, fmt.Errorf("invalid storage protocol version or container ID")
	}
	if !filepath.IsAbs(req.GraphRoot) || !filepath.IsAbs(req.RunRoot) {
		return request{}, fmt.Errorf("absolute storage roots required")
	}
	return req, nil
}

// CRI-O owns storage mount setup. Opening a reference must not make an
// existing overlay home private or change the daemon's mount propagation.
func referenceDriverOptions(options []string) []string {
	result := make([]string, 0, len(options)+1)
	for _, option := range options {
		if !strings.HasPrefix(option, "overlay.skip_mount_home=") {
			result = append(result, option)
		}
	}
	return append(result, "overlay.skip_mount_home=true")
}

func acquire(store storage.Store, id, pinID, metadata string) (source, error) {
	container, err := store.Container(id)
	if err != nil {
		return source{}, err
	}
	if err := validateSourceContainer(container); err != nil {
		return source{}, err
	}
	if err := pinImage(store, container.ImageID, pinID, metadata); err != nil {
		return source{}, err
	}
	chain, err := imageChain(store, container.ImageID)
	if err != nil {
		return source{}, err
	}
	driver, err := store.GraphDriver()
	if err != nil {
		return source{}, err
	}
	paths, err := driver.Metadata(container.LayerID)
	if err != nil {
		return source{}, err
	}
	result := source{ImageID: container.ImageID, LayerID: container.LayerID, RootfsChainID: chain, MountPoint: paths["MergedDir"], UpperDir: paths["UpperDir"], WorkDir: paths["WorkDir"]}
	result.LowerDirs, err = resolveLowerDirs(paths["LowerDir"])
	if err != nil {
		return source{}, err
	}
	return result, nil
}

func validateSourceContainer(container *storage.Container) error {
	if container.ImageID == "" || len(container.UIDMap) != 0 || len(container.GIDMap) != 0 {
		return fmt.Errorf("CRI-O image-backed, non-remapped storage is required")
	}
	var owner struct {
		PodID   string `json:"pod-id"`
		PodName string `json:"pod-name"`
	}
	if err := json.Unmarshal([]byte(container.Metadata), &owner); err != nil || owner.PodID == "" || owner.PodName == "" {
		return fmt.Errorf("source is not a CRI-O storage container")
	}
	return nil
}

func resolveLowerDirs(lower string) ([]string, error) {
	if lower == "" {
		return nil, fmt.Errorf("image has no overlay lower layer")
	}
	var dirs []string
	for _, path := range strings.Split(lower, ":") {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, resolved)
	}
	return dirs, nil
}

func pinImage(store storage.Store, imageID, id, metadata string) error {
	pin, err := store.Container(id)
	if errors.Is(err, storage.ErrContainerUnknown) {
		pin, err = store.CreateContainer(id, nil, imageID, "", metadata, nil)
	}
	if err != nil {
		return err
	}
	if pin.Metadata != metadata || pin.ImageID != imageID {
		return fmt.Errorf("RootPV storage reference identity mismatch")
	}
	return nil
}

func release(store storage.Store, id, metadata string) error {
	pin, err := store.Container(id)
	if errors.Is(err, storage.ErrContainerUnknown) {
		return nil
	}
	if err != nil {
		return err
	}
	if pin.Metadata != metadata {
		return fmt.Errorf("refusing to delete an unowned storage container")
	}
	return store.DeleteContainer(id)
}

func imageChain(store storage.Store, id string) (string, error) {
	config, err := readVerifiedImageConfig(store, id)
	if err != nil {
		return "", err
	}
	var image imagev1.Image
	if err := json.Unmarshal(config, &image); err != nil {
		return "", err
	}
	if image.RootFS.Type != "layers" || len(image.RootFS.DiffIDs) == 0 {
		return "", fmt.Errorf("image layer identities unavailable")
	}
	for _, diff := range image.RootFS.DiffIDs {
		if err := diff.Validate(); err != nil {
			return "", err
		}
	}
	return identity.ChainID(image.RootFS.DiffIDs).String(), nil
}

func readVerifiedImageConfig(store storage.Store, id string) ([]byte, error) {
	blob, err := store.ImageBigData(id, storage.ImageDigestBigDataKey)
	if err != nil {
		return nil, err
	}
	var manifest imagev1.Manifest
	if err := json.Unmarshal(blob, &manifest); err != nil {
		return nil, err
	}
	if err := manifest.Config.Digest.Validate(); err != nil {
		return nil, err
	}
	config, err := store.ImageBigData(id, manifest.Config.Digest.String())
	if err != nil {
		return nil, err
	}
	if digest.FromBytes(config) != manifest.Config.Digest {
		return nil, fmt.Errorf("image config digest mismatch")
	}
	return config, nil
}
