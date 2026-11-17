// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package main

import (
	"fmt"
	"reflect"
	"testing"

	digest "github.com/opencontainers/go-digest"
	storage "go.podman.io/storage"
)

// Embed the unused Store methods; these cases exercise metadata, not mounts.
type referenceStore struct {
	storage.Store
	pin              *storage.Container
	manifest, config []byte
	deletes          int
}

func (s *referenceStore) Container(string) (*storage.Container, error) {
	if s.pin == nil {
		return nil, storage.ErrContainerUnknown
	}
	return s.pin, nil
}

func (s *referenceStore) DeleteContainer(string) error {
	s.deletes++
	s.pin = nil
	return nil
}

func (s *referenceStore) ImageBigData(_, key string) ([]byte, error) {
	if key == storage.ImageDigestBigDataKey {
		return s.manifest, nil
	}
	return s.config, nil
}

func TestReferenceOwnershipAndRelease(t *testing.T) {
	store := &referenceStore{pin: &storage.Container{Metadata: "another-owner", ImageID: "image"}}
	if err := release(store, "pin", "owner"); err == nil || store.deletes != 0 {
		t.Fatal("unowned reference must not be deleted")
	}
	if err := pinImage(store, "image", "pin", "owner"); err == nil {
		t.Fatal("unowned reference must not be reused")
	}
	store.pin.Metadata = "owner"
	if err := pinImage(store, "different-image", "pin", "owner"); err == nil {
		t.Fatal("a reference to another image must not be reused")
	}
	if err := pinImage(store, "image", "pin", "owner"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := release(store, "pin", "owner"); err != nil {
			t.Fatal(err)
		}
	}
	if store.deletes != 1 {
		t.Fatalf("idempotent release performed %d deletions", store.deletes)
	}
}

func TestImageChainValidation(t *testing.T) {
	diff := digest.FromString("image-layer")
	valid := []byte(fmt.Sprintf(`{"rootfs":{"type":"layers","diff_ids":["%s"]}}`, diff))
	for _, tc := range []struct {
		name, want string
		config     []byte
		manifest   digest.Digest
	}{
		{"valid", diff.String(), valid, digest.FromBytes(valid)},
		{"corrupt config", "", valid, digest.FromString("different-config")},
		{"empty layers", "", []byte(`{"rootfs":{"type":"layers"}}`), digest.FromString(`{"rootfs":{"type":"layers"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &referenceStore{config: tc.config, manifest: []byte(fmt.Sprintf(`{"config":{"digest":"%s"}}`, tc.manifest))}
			chain, err := imageChain(store, "image")
			if (err == nil) != (tc.want != "") || chain != tc.want {
				t.Fatalf("chain=%q, err=%v; want %q", chain, err, tc.want)
			}
		})
	}
}

func TestReferenceDriverOptionsPreserveMountSharing(t *testing.T) {
	options := []string{"overlay.skip_mount_home=false", "overlay.size=1G", "overlay.skip_mount_home=true"}
	want := []string{"overlay.size=1G", "overlay.skip_mount_home=true"}
	if got := referenceDriverOptions(options); !reflect.DeepEqual(got, want) {
		t.Fatalf("driver options=%v; want %v", got, want)
	}
}
