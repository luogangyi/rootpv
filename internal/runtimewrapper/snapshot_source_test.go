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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
)

const testRootfsChainID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestSnapshotSourceResolvesActiveOverlayMount(t *testing.T) {
	client := validFakeSnapshotMetadataClient()

	source := ContainerdSnapshotSource{Client: client, Namespace: "k8s.io"}
	got, err := source.Resolve(context.Background(), "container-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Snapshotter != "overlayfs" || got.SnapshotKey != "container-1" || got.RootfsChainID != testRootfsChainID {
		t.Fatalf("unexpected snapshot identity: %+v", got)
	}
	wantLower := []string{"/snapshots/3/fs", "/snapshots/2/fs", "/snapshots/1/fs"}
	if strings.Join(got.LowerDirs, ":") != strings.Join(wantLower, ":") {
		t.Fatalf("lowerdir order changed: got %v want %v", got.LowerDirs, wantLower)
	}
	if got.UpperDir != "/snapshots/4/fs" || got.WorkDir != "/snapshots/4/work" {
		t.Fatalf("unexpected active dirs: %+v", got)
	}
	if client.namespace != "k8s.io" || client.containerID != "container-1" {
		t.Fatalf("unexpected request namespace=%q container=%q", client.namespace, client.containerID)
	}
}

func TestSnapshotSourceRejectsInvalidMetadataAndMounts(t *testing.T) {
	assertInvalidSnapshotSources(t, invalidSnapshotMetadataCases())
	assertInvalidSnapshotSources(t, invalidSnapshotMountCases())
}

type invalidSnapshotSourceCase struct {
	name       string
	mutate     func(*fakeSnapshotMetadataClient)
	wantReason string
}

func invalidSnapshotMetadataCases() []invalidSnapshotSourceCase {
	return []invalidSnapshotSourceCase{
		{
			name: "non overlay snapshotter",
			mutate: func(c *fakeSnapshotMetadataClient) {
				c.snapshotter = "native"
			},
			wantReason: "snapshotter_unsupported",
		},
		{
			name: "committed snapshot",
			mutate: func(c *fakeSnapshotMetadataClient) {
				c.info.Kind = snapshots.KindCommitted
			},
			wantReason: "snapshot_not_active",
		},
		{
			name: "invalid parent",
			mutate: func(c *fakeSnapshotMetadataClient) {
				c.info.Parent = "latest"
			},
			wantReason: "snapshot_parent_invalid",
		},
	}
}

func invalidSnapshotMountCases() []invalidSnapshotSourceCase {
	return []invalidSnapshotSourceCase{
		{name: "bind mount", mutate: func(c *fakeSnapshotMetadataClient) { c.mounts[0].Type = "bind" }, wantReason: "source_filesystem_not_overlay"},
		{
			name: "multiple mounts",
			mutate: func(c *fakeSnapshotMetadataClient) {
				c.mounts = append(c.mounts, c.mounts[0])
			},
			wantReason: "source_overlay_options_invalid",
		},
		{
			name: "duplicate lower",
			mutate: func(c *fakeSnapshotMetadataClient) {
				c.mounts[0].Options = append(c.mounts[0].Options, "lowerdir=/snapshots/other/fs")
			},
			wantReason: "source_overlay_options_invalid",
		},
		{
			name: "missing work",
			mutate: func(c *fakeSnapshotMetadataClient) {
				c.mounts[0].Options = []string{
					"lowerdir=/snapshots/3/fs:/snapshots/2/fs",
					"upperdir=/snapshots/4/fs",
				}
			},
			wantReason: "source_overlay_options_invalid",
		},
		{
			name: "relative lower",
			mutate: func(c *fakeSnapshotMetadataClient) {
				c.mounts[0].Options[1] = "lowerdir=snapshots/3/fs:/snapshots/2/fs"
			},
			wantReason: "source_overlay_options_invalid",
		},
	}
}

func assertInvalidSnapshotSources(t *testing.T, tests []invalidSnapshotSourceCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := validFakeSnapshotMetadataClient()
			tt.mutate(client)
			_, err := (ContainerdSnapshotSource{Client: client, Namespace: "k8s.io"}).Resolve(context.Background(), "container-1")
			if err == nil || !strings.Contains(err.Error(), tt.wantReason) {
				t.Fatalf("expected reason %q, got %v", tt.wantReason, err)
			}
		})
	}
}

func TestSnapshotSourcePropagatesMetadataFailure(t *testing.T) {
	want := errors.New("containerd unavailable")
	client := validFakeSnapshotMetadataClient()
	client.err = want
	_, err := (ContainerdSnapshotSource{Client: client}).Resolve(context.Background(), "container-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected wrapped metadata error, got %v", err)
	}
}

type fakeSnapshotMetadataClient struct {
	namespace   string
	containerID string
	snapshotter string
	snapshotKey string
	info        snapshots.Info
	mounts      []mount.Mount
	err         error
}

func validFakeSnapshotMetadataClient() *fakeSnapshotMetadataClient {
	return &fakeSnapshotMetadataClient{
		snapshotter: "overlayfs",
		snapshotKey: "container-1",
		info: snapshots.Info{
			Kind:   snapshots.KindActive,
			Name:   "container-1",
			Parent: testRootfsChainID,
		},
		mounts: []mount.Mount{{
			Type:   "overlay",
			Source: "overlay",
			Options: []string{
				"index=off",
				"lowerdir=/snapshots/3/fs:/snapshots/2/fs:/snapshots/1/fs",
				"upperdir=/snapshots/4/fs",
				"workdir=/snapshots/4/work",
			},
		}},
	}
}

func (c *fakeSnapshotMetadataClient) ContainerSnapshot(_ context.Context, namespace, containerID string) (string, string, error) {
	c.namespace = namespace
	c.containerID = containerID
	return c.snapshotter, c.snapshotKey, c.err
}

func (c *fakeSnapshotMetadataClient) SnapshotInfo(context.Context, string, string, string) (snapshots.Info, []mount.Mount, error) {
	return c.info, c.mounts, c.err
}
