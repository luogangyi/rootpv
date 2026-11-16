//go:build !linux

// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package crio

import (
	"context"
	"fmt"
)

func (c Client) PeerIdentity(context.Context) (string, error) {
	return "", fmt.Errorf("CRI-O activation requires Linux")
}
