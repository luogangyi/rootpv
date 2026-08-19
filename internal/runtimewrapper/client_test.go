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
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
)

func TestUnixResolverPostsResolveRequest(t *testing.T) {
	socket := fmt.Sprintf("%s/rootfsd-%d.sock", os.TempDir(), time.Now().UnixNano())
	defer os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	var got apiv1alpha1.ResolveRootfsRequest
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rootfs/resolve" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if err := json.NewEncoder(w).Encode(apiv1alpha1.ResolveRootfsResponse{
			Enabled:  true,
			UpperDir: "/pvc/upper",
			WorkDir:  "/pvc/work",
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	})}
	go server.Serve(ln)
	defer server.Shutdown(context.Background())

	resolver := NewUnixResolver(socket)
	resp, err := resolver.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{ContainerID: "container-id"})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if got.ContainerID != "container-id" {
		t.Fatalf("unexpected request: %+v", got)
	}
	if !resp.Enabled || resp.UpperDir != "/pvc/upper" || resp.WorkDir != "/pvc/work" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestUnixResolverIncludesConflictResponseBody(t *testing.T) {
	socket := fmt.Sprintf("%s/rootfsd-%d.sock", os.TempDir(), time.Now().UnixNano())
	defer os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `persistent rootfs pod "pod-uid" is not registered`, http.StatusConflict)
	})}
	go server.Serve(ln)
	defer server.Shutdown(context.Background())

	resolver := NewUnixResolver(socket)
	_, err = resolver.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{ContainerID: "container-id"})
	if err == nil {
		t.Fatalf("expected conflict error")
	}
	if !strings.Contains(err.Error(), "409 Conflict") ||
		!strings.Contains(err.Error(), `persistent rootfs pod "pod-uid" is not registered`) {
		t.Fatalf("expected status and response body in error, got %q", err)
	}
}

func TestUnixResolverPostsLifecycleRequests(t *testing.T) {
	socket := fmt.Sprintf("%s/rootfsd-%d.sock", os.TempDir(), time.Now().UnixNano())
	defer os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	paths := make(chan string, 4)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		switch r.URL.Path {
		case "/rootfs/mounted":
			var req apiv1alpha1.MarkMountedRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LeaseID != "lease" {
				t.Errorf("mounted request=%+v err=%v", req, err)
			}
			if err := json.NewEncoder(w).Encode(apiv1alpha1.MarkMountedResponse{Mounted: true}); err != nil {
				t.Errorf("encode mounted response: %v", err)
			}
		case "/rootfs/create/begin":
			var req apiv1alpha1.BeginCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ContainerID != "cid" {
				t.Errorf("begin request=%+v err=%v", req, err)
			}
			if err := json.NewEncoder(w).Encode(apiv1alpha1.BeginCreateResponse{AttemptToken: "token"}); err != nil {
				t.Errorf("encode begin response: %v", err)
			}
		case "/rootfs/create/register":
			var req apiv1alpha1.RegisterDelegateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AttemptToken != "token" {
				t.Errorf("register request=%+v err=%v", req, err)
			}
			if err := json.NewEncoder(w).Encode(apiv1alpha1.RegisterDelegateResponse{Registered: true}); err != nil {
				t.Errorf("encode register response: %v", err)
			}
		case "/rootfs/create/commit":
			var req apiv1alpha1.CommitCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AttemptToken != "token" {
				t.Errorf("commit request=%+v err=%v", req, err)
			}
			if err := json.NewEncoder(w).Encode(apiv1alpha1.CommitCreateResponse{Created: true}); err != nil {
				t.Errorf("encode commit response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	})}
	go server.Serve(ln)
	defer server.Shutdown(context.Background())

	resolver := NewUnixResolver(socket)
	if resp, err := resolver.MarkMounted(context.Background(), apiv1alpha1.MarkMountedRequest{LeaseID: "lease", RuntimeStatePath: "/run/state.json"}); err != nil || !resp.Mounted {
		t.Fatalf("MarkMounted: response=%+v err=%v", resp, err)
	}
	if resp, err := resolver.BeginCreate(context.Background(), apiv1alpha1.BeginCreateRequest{LeaseID: "lease", ContainerID: "cid", WrapperPID: 1, WrapperStartTime: 2}); err != nil || resp.AttemptToken != "token" {
		t.Fatalf("BeginCreate: response=%+v err=%v", resp, err)
	}
	if resp, err := resolver.RegisterDelegate(context.Background(), apiv1alpha1.RegisterDelegateRequest{LeaseID: "lease", AttemptToken: "token", DelegatePID: 3, DelegateStartTime: 4}); err != nil || !resp.Registered {
		t.Fatalf("RegisterDelegate: response=%+v err=%v", resp, err)
	}
	if resp, err := resolver.CommitCreate(context.Background(), apiv1alpha1.CommitCreateRequest{LeaseID: "lease", AttemptToken: "token"}); err != nil || !resp.Created {
		t.Fatalf("CommitCreate: response=%+v err=%v", resp, err)
	}

	want := []string{"/rootfs/mounted", "/rootfs/create/begin", "/rootfs/create/register", "/rootfs/create/commit"}
	for _, path := range want {
		if got := <-paths; got != path {
			t.Fatalf("path=%q want=%q", got, path)
		}
	}
}
