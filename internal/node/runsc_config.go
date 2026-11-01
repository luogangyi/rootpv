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

package node

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

const runscOptionsTypeURL = "io.containerd.runsc.v1.options"

// Pin the exact shim bytes used by discovery, the digest and installation.
type runscConfigSnapshot struct {
	raw                map[string]any
	binary             string
	content, bootstrap []byte
}

func loadRunscConfig(source map[string]any, hostRoot string) (*runscConfigSnapshot, error) {
	options := mapValue(source["options"])
	if stringValue(options["TypeUrl"]) != runscOptionsTypeURL {
		return nil, fmt.Errorf("runsc_options_type_required")
	}
	path := stringValue(options["ConfigPath"])
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("runsc_config_path_required")
	}
	content, err := os.ReadFile(hostPath(hostRoot, path))
	if err != nil {
		return nil, fmt.Errorf("runsc_config_unreadable: %s: %w", path, err)
	}
	raw := make(map[string]any)
	if _, err := toml.Decode(string(content), &raw); err != nil {
		return nil, fmt.Errorf("runsc_config_unreadable: %s: %w", path, err)
	}
	binary, ok := raw["binary_name"]
	if !ok || !filepath.IsAbs(stringValue(binary)) {
		return nil, fmt.Errorf("runsc_binary_path_required")
	}
	if value, ok := raw["runsc_config"]; ok {
		flags, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("runsc_flags_invalid")
		}
		for _, value := range flags {
			if _, ok := value.(string); !ok {
				return nil, fmt.Errorf("runsc_flags_invalid")
			}
		}
	}
	bootstrap, err := runscBootstrapEvidence(hostRoot)
	if err != nil {
		return nil, err
	}
	return &runscConfigSnapshot{raw: raw, binary: stringValue(binary), content: content, bootstrap: bootstrap}, nil
}

// The shim manager reads a global config before CreateTask supplies ConfigPath.
// Without grouping, CRI can leave unused per-container shim servers behind.
func runscBootstrapEvidence(hostRoot string) ([]byte, error) {
	for _, path := range []string{"/run/containerd/runsc/config.toml", "/etc/containerd/runsc/config.toml"} {
		data, err := os.ReadFile(hostPath(hostRoot, path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("runsc_shim_config_unreadable: %s: %w", path, err)
		}
		var opts struct {
			Grouping bool `toml:"grouping"`
		}
		if _, err := toml.Decode(string(data), &opts); err != nil {
			return nil, fmt.Errorf("runsc_shim_config_invalid: %s: %w", path, err)
		}
		if !opts.Grouping {
			return nil, fmt.Errorf("runsc_shim_grouping_required")
		}
		return append([]byte(path+"\x00"), data...), nil
	}
	return nil, fmt.Errorf("runsc_shim_grouping_required")
}

// buildRunscHandler keeps the vendor shim and gives it a private TOML file.
// gVisor's default overlay would otherwise hide writes from the PVC layer.
func buildRunscHandler(adapter config.CatalogRuntimeAdapter, source runtimeHandlerSource, opts RuntimeReconcileOptions, wrapper string) (map[string]any, error) {
	if source.runsc == nil {
		return nil, fmt.Errorf("validated runsc config is required")
	}
	shimConfig := cloneMap(source.runsc.raw)
	shimConfig["binary_name"] = wrapper
	flags := mapValue(shimConfig["runsc_config"])
	delete(flags, "overlay")
	flags["overlay2"] = "none"
	shimConfig["runsc_config"] = flags
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(shimConfig); err != nil {
		return nil, err
	}
	generated := filepath.Join(filepath.Dir(opts.RegistryPath), "runsc", adapter.ID+"-"+strings.TrimPrefix(sha256Hex(encoded.Bytes()), "sha256:")+".toml")
	// Sandboxes/tasks can retain old ConfigPath references after catalog removal.
	// Reclaim historical files only after draining and checking runtime references.
	// ReconcileRuntimeAdapters tracks writes through beforeWrite, including
	// recreation of a missing file at an unchanged ConfigPath.
	if _, err := writeFileWithHook(generated, encoded.Bytes(), 0o600, opts.beforeWrite); err != nil {
		return nil, err
	}
	realGenerated, err := filepath.EvalSymlinks(generated)
	if err != nil {
		return nil, err
	}
	clone := cloneMap(source.table)
	clone["pod_annotations"] = appendStringValue(clone["pod_annotations"], "openfuyao.cn/*")
	clone["container_annotations"] = appendStringValue(clone["container_annotations"], "openfuyao.cn/*")
	options := mapValue(clone["options"])
	delete(options, "BinaryName")
	options["ConfigPath"] = fromHostPath(opts.HostRoot, realGenerated)
	clone["options"] = options
	return clone, nil
}
