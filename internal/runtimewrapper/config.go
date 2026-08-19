/*
 * Copyright (c) 2026 OpenFuyao Contributors.
 * RootPV is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 * http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 * See the Mulan PSL v2 for more details.
 */

package runtimewrapper

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigPath          = "/etc/rootpv/rootfs-runtime-wrapper.yaml"
	DefaultRootfsdSocket       = "/run/persistent-rootfsd/rootfsd.sock"
	DefaultRuntimeRoot         = "/run/rootpv/rootfs"
	DefaultContainerdAddress   = "/run/containerd/containerd.sock"
	DefaultContainerdNamespace = "k8s.io"
	DefaultOverlaySnapshotRoot = "/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots"
	OverlayModeNestedRootfs    = "nested-rootfs"
	OverlayModeFlatLowerdirs   = "flat-lowerdirs"
)

type Config struct {
	DefaultDelegateRuntime      string            `yaml:"defaultDelegateRuntime"`
	RootfsdSocket               string            `yaml:"rootfsdSocket"`
	RuntimeRoot                 string            `yaml:"runtimeRoot"`
	OverlayMode                 string            `yaml:"overlayMode"`
	ContainerdAddress           string            `yaml:"containerdAddress"`
	ContainerdNamespace         string            `yaml:"containerdNamespace"`
	AllowedOverlaySnapshotRoots []string          `yaml:"allowedOverlaySnapshotRoots"`
	DelegateRuntimeByWrapper    map[string]string `yaml:"delegateRuntimeByWrapper"`
	RuntimeAdapterCutover       bool              `yaml:"runtimeAdapterCutover"`
	RuntimeAdapterRegistry      string            `yaml:"runtimeAdapterRegistry"`
	AllowedDelegateDirs         []string          `yaml:"allowedDelegateDirs"`
}

func DefaultConfig() Config {
	return Config{
		DefaultDelegateRuntime: "/usr/bin/runc",
		RootfsdSocket:          DefaultRootfsdSocket,
		RuntimeRoot:            DefaultRuntimeRoot,
		OverlayMode:            OverlayModeNestedRootfs,
		ContainerdAddress:      DefaultContainerdAddress,
		ContainerdNamespace:    DefaultContainerdNamespace,
		AllowedOverlaySnapshotRoots: []string{
			DefaultOverlaySnapshotRoot,
		},
		DelegateRuntimeByWrapper: map[string]string{
			"rootfs-runtime-wrapper":        "/usr/bin/runc",
			"rootfs-runtime-wrapper-runc":   "/usr/bin/runc",
			"rootfs-runtime-wrapper-nvidia": "/usr/bin/nvidia-container-runtime",
			"rootpv-runtime-runc":           "/usr/bin/runc",
			"rootpv-runtime-nvidia":         "/usr/bin/nvidia-container-runtime",
		},
	}
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		path = DefaultConfigPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return Config{}, err
	}
	var fileCfg Config
	if err := yaml.Unmarshal(data, &fileCfg); err != nil {
		return Config{}, err
	}
	if fileCfg.DefaultDelegateRuntime != "" {
		cfg.DefaultDelegateRuntime = fileCfg.DefaultDelegateRuntime
	}
	if fileCfg.RootfsdSocket != "" {
		cfg.RootfsdSocket = fileCfg.RootfsdSocket
	}
	if fileCfg.RuntimeRoot != "" {
		cfg.RuntimeRoot = fileCfg.RuntimeRoot
	}
	if fileCfg.OverlayMode != "" {
		cfg.OverlayMode = fileCfg.OverlayMode
	}
	if fileCfg.ContainerdAddress != "" {
		cfg.ContainerdAddress = fileCfg.ContainerdAddress
	}
	if fileCfg.ContainerdNamespace != "" {
		cfg.ContainerdNamespace = fileCfg.ContainerdNamespace
	}
	if len(fileCfg.AllowedOverlaySnapshotRoots) > 0 {
		cfg.AllowedOverlaySnapshotRoots = append([]string(nil), fileCfg.AllowedOverlaySnapshotRoots...)
	}
	if len(fileCfg.DelegateRuntimeByWrapper) > 0 {
		for k, v := range fileCfg.DelegateRuntimeByWrapper {
			cfg.DelegateRuntimeByWrapper[k] = v
		}
	}
	cfg.RuntimeAdapterCutover = fileCfg.RuntimeAdapterCutover
	if fileCfg.RuntimeAdapterRegistry != "" {
		cfg.RuntimeAdapterRegistry = fileCfg.RuntimeAdapterRegistry
	}
	if len(fileCfg.AllowedDelegateDirs) > 0 {
		cfg.AllowedDelegateDirs = append([]string(nil), fileCfg.AllowedDelegateDirs...)
	}
	return cfg, nil
}

func LoadConfigFromEnv() (Config, error) {
	path := os.Getenv("ROOTPV_WRAPPER_CONFIG")
	cfg, err := LoadConfig(path)
	if err != nil {
		return Config{}, err
	}
	if value := os.Getenv("ROOTPV_DELEGATE_RUNTIME"); value != "" {
		cfg.DefaultDelegateRuntime = value
	}
	if value := os.Getenv("ROOTPV_ROOTFSD_SOCKET"); value != "" {
		cfg.RootfsdSocket = value
	}
	if value := os.Getenv("ROOTPV_RUNTIME_ROOT"); value != "" {
		cfg.RuntimeRoot = value
	}
	if value := os.Getenv("ROOTPV_OVERLAY_MODE"); value != "" {
		cfg.OverlayMode = value
	}
	if value := os.Getenv("ROOTPV_CONTAINERD_ADDRESS"); value != "" {
		cfg.ContainerdAddress = value
	}
	if value := os.Getenv("ROOTPV_CONTAINERD_NAMESPACE"); value != "" {
		cfg.ContainerdNamespace = value
	}
	return cfg, nil
}

func (c Config) DelegateRuntime(wrapperName string) (string, error) {
	base := filepath.Base(wrapperName)
	if c.DelegateRuntimeByWrapper != nil && c.DelegateRuntimeByWrapper[base] != "" {
		return c.DelegateRuntimeByWrapper[base], nil
	}
	return c.DefaultDelegateRuntime, nil
}
