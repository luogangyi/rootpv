//go:build !linux

// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package runtimewrapper

import "fmt"

func copyCRIOBootstrap(_, _ string) error { return fmt.Errorf("CRI-O requires Linux") }
