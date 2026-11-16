// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package crio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseTimeoutRetainsIntent(t *testing.T) {
	root, id := t.TempDir(), strings.Repeat("a", 64)
	helper := filepath.Join(root, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "record.json")
	data := `{"request":{"version":1,"containerID":"` + id + `"},"socket":"/test/crio.sock"}`
	if err := os.WriteFile(record, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	store := Storage{Client: Client{Socket: "/test/crio.sock"}, Helper: helper, StateRoot: root}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := store.Release(ctx, id); err == nil || ctx.Err() == nil {
		t.Fatalf("expected helper deadline failure, got %v", err)
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("failed release lost its recovery intent: %v", err)
	}
}
