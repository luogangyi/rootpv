// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package crio

import "testing"

func TestRejectAutomaticStorageDeletion(t *testing.T) {
	root := map[string]any{"internal_wipe": false, "internal_repair": false}
	raw := map[string]any{"crio": root}
	if err := ValidateStorageSafety(raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"internal_wipe", "internal_repair"} {
		root[key] = true
		if err := ValidateStorageSafety(raw); err == nil {
			t.Fatalf("%s enabled must reject new persistent roots", key)
		}
		root[key] = false
	}
	delete(root, "internal_repair")
	if err := ValidateStorageSafety(raw); err == nil {
		t.Fatal("unknown storage repair policy must reject new persistent roots")
	}
}
