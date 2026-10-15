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

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

const (
	RuntimeAdapterFormatVersion = "rootpv-runtime-adapters-v1"
	RuncV2RuntimeType           = "io.containerd.runc.v2"
	KataV2RuntimeType           = "io.containerd.kata.v2"
	KataQemuRuntimeType         = "io.containerd.kata-qemu.v2"
	maxRuntimeAdapterIDLength   = 48
)

type RuntimeAdapterMode string

const (
	RuntimeAdapterModeExplicit    RuntimeAdapterMode = "Explicit"
	RuntimeAdapterModeNodeDefault RuntimeAdapterMode = "NodeDefault"
)

type DefaultRuntimePolicy string

const (
	DefaultRuntimePolicyNodeDefault DefaultRuntimePolicy = "NodeDefault"
	DefaultRuntimePolicyFixed       DefaultRuntimePolicy = "Fixed"
)

type RuntimeAdapter struct {
	ID                   string             `yaml:"id" json:"id"`
	Mode                 RuntimeAdapterMode `yaml:"mode" json:"mode"`
	OriginalRuntimeClass string             `yaml:"originalRuntimeClass,omitempty" json:"originalRuntimeClass,omitempty"`
	OriginalHandler      string             `yaml:"originalHandler,omitempty" json:"originalHandler,omitempty"`
	RootfsRuntimeClass   string             `yaml:"rootpvRuntimeClass" json:"rootpvRuntimeClass"`
	RootfsHandler        string             `yaml:"rootpvHandler" json:"rootpvHandler"`
	RuntimeType          string             `yaml:"runtimeType" json:"runtimeType"`
	DelegateBinary       string             `yaml:"delegateBinary,omitempty" json:"delegateBinary,omitempty"`
	ConfigDigest         string             `yaml:"configDigest,omitempty" json:"configDigest,omitempty"`
}

type RuntimeAdapterConfig struct {
	DefaultRuntimePolicy DefaultRuntimePolicy `yaml:"defaultRuntimePolicy"`
	FixedDefaultAdapter  string               `yaml:"fixedDefaultAdapter,omitempty"`
	Adapters             []RuntimeAdapter     `yaml:"runtimeAdapters"`
}

var runtimeAdapterIDPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func BuiltInNodeDefaultAdapter() RuntimeAdapter {
	return RuntimeAdapter{
		ID:                 "node-default",
		Mode:               RuntimeAdapterModeNodeDefault,
		RootfsRuntimeClass: "persistent-rootfs-node-default",
		RootfsHandler:      "persistent-rootfs-node-default",
		RuntimeType:        RuncV2RuntimeType,
	}
}

func (c *RuntimeAdapterConfig) Validate() error {
	if c.DefaultRuntimePolicy == "" {
		c.DefaultRuntimePolicy = DefaultRuntimePolicyNodeDefault
	}
	if c.DefaultRuntimePolicy != DefaultRuntimePolicyNodeDefault && c.DefaultRuntimePolicy != DefaultRuntimePolicyFixed {
		return fmt.Errorf("unsupported default runtime policy %q", c.DefaultRuntimePolicy)
	}
	byID := make(map[string]struct{}, len(c.Adapters))
	byOriginal := make(map[string]struct{}, len(c.Adapters))
	byShadowClass := make(map[string]struct{}, len(c.Adapters))
	byShadowHandler := make(map[string]struct{}, len(c.Adapters))
	for _, adapter := range c.Adapters {
		if err := adapter.ValidateExplicit(); err != nil {
			return err
		}
		for _, unique := range []struct {
			name  string
			value string
			seen  map[string]struct{}
		}{
			{name: "id", value: adapter.ID, seen: byID},
			{name: "original runtimeClass", value: adapter.OriginalRuntimeClass, seen: byOriginal},
			{name: "rootpv runtimeClass", value: adapter.RootfsRuntimeClass, seen: byShadowClass},
			{name: "rootpv handler", value: adapter.RootfsHandler, seen: byShadowHandler},
		} {
			if _, ok := unique.seen[unique.value]; ok {
				return fmt.Errorf("duplicate runtime adapter %s %q", unique.name, unique.value)
			}
			unique.seen[unique.value] = struct{}{}
		}
	}
	if c.DefaultRuntimePolicy == DefaultRuntimePolicyFixed {
		if c.FixedDefaultAdapter == "" {
			return fmt.Errorf("fixed default adapter is required")
		}
		if _, ok := byID[c.FixedDefaultAdapter]; !ok {
			return fmt.Errorf("fixed default adapter %q is not registered", c.FixedDefaultAdapter)
		}
	}
	return nil
}

func (a RuntimeAdapter) ValidateExplicit() error {
	if a.ID == "" || len(a.ID) > maxRuntimeAdapterIDLength || !runtimeAdapterIDPattern.MatchString(a.ID) {
		return fmt.Errorf("invalid runtime adapter id %q", a.ID)
	}
	if a.ID == "node-default" || a.Mode == RuntimeAdapterModeNodeDefault {
		return fmt.Errorf("runtime adapter %q uses reserved node-default mode", a.ID)
	}
	if a.Mode != RuntimeAdapterModeExplicit {
		return fmt.Errorf("unsupported runtime adapter mode %q", a.Mode)
	}
	if a.OriginalRuntimeClass == "" || a.OriginalHandler == "" || a.RootfsRuntimeClass == "" || a.RootfsHandler == "" || a.DelegateBinary == "" {
		return fmt.Errorf("runtime adapter %q has required fields missing", a.ID)
	}
	if a.RuntimeType != RuncV2RuntimeType {
		return fmt.Errorf("runtime adapter %q has unsupported runtime type %q", a.ID, a.RuntimeType)
	}
	return nil
}

func (c RuntimeAdapterConfig) Resolve(originalRuntimeClass string) (RuntimeAdapter, error) {
	if err := c.Validate(); err != nil {
		return RuntimeAdapter{}, err
	}
	if originalRuntimeClass == "" {
		if c.DefaultRuntimePolicy == "" || c.DefaultRuntimePolicy == DefaultRuntimePolicyNodeDefault {
			return BuiltInNodeDefaultAdapter(), nil
		}
		for _, adapter := range c.Adapters {
			if adapter.ID == c.FixedDefaultAdapter {
				return adapter, nil
			}
		}
		return RuntimeAdapter{}, fmt.Errorf("fixed default adapter %q is not registered", c.FixedDefaultAdapter)
	}
	for _, adapter := range c.Adapters {
		if adapter.OriginalRuntimeClass == originalRuntimeClass {
			return adapter, nil
		}
	}
	return RuntimeAdapter{}, fmt.Errorf("runtime adapter not found for runtimeClass %q", originalRuntimeClass)
}

func (a RuntimeAdapter) ClusterDigest() (string, error) {
	if a.ConfigDigest != "" {
		return a.ConfigDigest, nil
	}
	if a.Mode == RuntimeAdapterModeNodeDefault {
		a = BuiltInNodeDefaultAdapter()
	} else {
		if err := a.ValidateExplicit(); err != nil {
			return "", err
		}
	}
	payload := struct {
		FormatVersion        string             `json:"formatVersion"`
		ID                   string             `json:"id"`
		Mode                 RuntimeAdapterMode `json:"mode"`
		OriginalRuntimeClass string             `json:"originalRuntimeClass,omitempty"`
		OriginalHandler      string             `json:"originalHandler,omitempty"`
		RootfsRuntimeClass   string             `json:"rootpvRuntimeClass"`
		RootfsHandler        string             `json:"rootpvHandler"`
		RuntimeType          string             `json:"runtimeType"`
	}{
		FormatVersion:        RuntimeAdapterFormatVersion,
		ID:                   a.ID,
		Mode:                 a.Mode,
		OriginalRuntimeClass: a.OriginalRuntimeClass,
		OriginalHandler:      a.OriginalHandler,
		RootfsRuntimeClass:   a.RootfsRuntimeClass,
		RootfsHandler:        a.RootfsHandler,
		RuntimeType:          a.RuntimeType,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// IsKataQemuRuntimeType deliberately excludes CLH and unrelated task shims.
func IsKataQemuRuntimeType(runtimeType string) bool {
	return runtimeType == KataQemuRuntimeType || runtimeType == KataV2RuntimeType
}

func SupportedRuntimeType(runtimeType string) bool {
	return runtimeType == RuncV2RuntimeType || IsKataQemuRuntimeType(runtimeType)
}
