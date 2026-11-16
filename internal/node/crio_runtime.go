// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package node

import (
	"fmt"
	"path/filepath"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

type crioRuntimeSource struct {
	runtimeType, delegate string
}

// This provider supports the native runc OCI path only. VM handlers remain
// available to other providers but are never advertised by CRI-O discovery.
func loadCRIORuntimeSource(table map[string]any) (crioRuntimeSource, error) {
	source := crioRuntimeSource{delegate: stringValue(table["runtime_path"])}
	if len(mapValue(table["platform_runtime_paths"])) != 0 {
		return source, fmt.Errorf("unsupported_crio_platform_paths")
	}
	if stringValue(table["runtime_type"]) != "oci" {
		return source, fmt.Errorf("unsupported_crio_runtime_type")
	}
	if filepath.Base(source.delegate) != "runc" {
		return source, fmt.Errorf("unsupported_crio_oci_runtime: runc required")
	}
	source.runtimeType = config.RuncV2RuntimeType
	return source, nil
}

func buildCRIOShadow(adapter config.CatalogRuntimeAdapter, table map[string]any, opts RuntimeReconcileOptions) (map[string]any, error) {
	if !executable(hostPath(opts.HostRoot, opts.WrapperBinary)) {
		return nil, fmt.Errorf("runtime wrapper is unavailable")
	}
	alias, err := ensureAdapterWrapper(opts, adapter.ID)
	if err != nil {
		return nil, err
	}
	shadow := cloneConfigMap(table)
	shadow["inherit_default_runtime"] = false
	shadow["runtime_path"] = alias
	return shadow, nil
}
