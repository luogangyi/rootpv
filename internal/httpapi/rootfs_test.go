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

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime"

	rootfsapi "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/testutil"
)

func TestRootfsHandlerResolveAndRelease(t *testing.T) {
	fake := &fakeRootfsService{
		resolveResp: rootfsapi.ResolveRootfsResponse{
			Enabled:  true,
			UpperDir: "/pvc/root/upper",
			WorkDir:  "/pvc/root/work",
			MetaDir:  "/pvc/root/meta",
		},
		releaseResp: rootfsapi.ReleaseRootfsResponse{Released: true, Reason: "clean shutdown recorded"},
	}
	handler := NewRootfsHandler(fake)

	resolveReq := rootfsapi.ResolveRootfsRequest{PodUID: "pod-uid", ContainerName: "dev"}
	resolveRecorder := postJSON(t, handler, "/rootfs/resolve", resolveReq)
	if resolveRecorder.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", resolveRecorder.Code, resolveRecorder.Body.String())
	}
	if fake.resolveReq.PodUID != "pod-uid" || fake.resolveReq.ContainerName != "dev" {
		t.Fatalf("resolve request not passed through: %+v", fake.resolveReq)
	}
	var resolveResp rootfsapi.ResolveRootfsResponse
	if err := json.NewDecoder(resolveRecorder.Body).Decode(&resolveResp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if !resolveResp.Enabled || resolveResp.UpperDir != "/pvc/root/upper" {
		t.Fatalf("unexpected resolve response: %+v", resolveResp)
	}

	releaseReq := rootfsapi.ReleaseRootfsRequest{ContainerID: "cid", MetaDir: "/pvc/root/meta"}
	releaseRecorder := postJSON(t, handler, "/rootfs/release", releaseReq)
	if releaseRecorder.Code != http.StatusOK {
		t.Fatalf("release status=%d body=%s", releaseRecorder.Code, releaseRecorder.Body.String())
	}
	if fake.releaseReq.ContainerID != "cid" || fake.releaseReq.MetaDir != "/pvc/root/meta" {
		t.Fatalf("release request not passed through: %+v", fake.releaseReq)
	}
}

func TestRootfsHandlerReturnsConflictOnResolveError(t *testing.T) {
	fake := &fakeRootfsService{resolveErr: errors.New("not registered")}
	recorder := postJSON(t, NewRootfsHandler(fake), "/rootfs/resolve", rootfsapi.ResolveRootfsRequest{})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRootfsHandlerLifecycleRoutes(t *testing.T) {
	fake := &fakeRootfsService{}
	handler := NewRootfsHandler(fake)
	for _, test := range lifecycleRouteCases(fake) {
		t.Run(test.path, func(t *testing.T) {
			recorder := postJSON(t, handler, test.path, test.req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			test.check(t)
		})
	}
}

type lifecycleRouteCase struct {
	path  string
	req   any
	check func(*testing.T)
}

func lifecycleRouteCases(fake *fakeRootfsService) []lifecycleRouteCase {
	return []lifecycleRouteCase{
		{
			path: "/rootfs/mounted",
			req:  rootfsapi.MarkMountedRequest{LeaseID: "lease", RuntimeStatePath: "/run/state.json"},
			check: func(t *testing.T) {
				if fake.mountedReq.LeaseID != "lease" || fake.mountedReq.RuntimeStatePath != "/run/state.json" {
					t.Fatalf("unexpected mounted request: %+v", fake.mountedReq)
				}
			},
		},
		{
			path: "/rootfs/create/begin",
			req:  rootfsapi.BeginCreateRequest{LeaseID: "lease", ContainerID: "cid", WrapperPID: 10, WrapperStartTime: 20},
			check: func(t *testing.T) {
				if fake.beginReq.ContainerID != "cid" || fake.beginReq.WrapperStartTime != 20 {
					t.Fatalf("unexpected begin request: %+v", fake.beginReq)
				}
			},
		},
		{
			path: "/rootfs/create/register",
			req:  rootfsapi.RegisterDelegateRequest{LeaseID: "lease", AttemptToken: "token", DelegatePID: 30, DelegateStartTime: 40},
			check: func(t *testing.T) {
				if fake.registerReq.AttemptToken != "token" || fake.registerReq.DelegatePID != 30 {
					t.Fatalf("unexpected register request: %+v", fake.registerReq)
				}
			},
		},
		{
			path: "/rootfs/create/commit",
			req:  rootfsapi.CommitCreateRequest{LeaseID: "lease", AttemptToken: "token"},
			check: func(t *testing.T) {
				if fake.commitReq.LeaseID != "lease" || fake.commitReq.AttemptToken != "token" {
					t.Fatalf("unexpected commit request: %+v", fake.commitReq)
				}
			},
		},
	}
}

func TestAdmissionHandlerServesMutationIndependently(t *testing.T) {
	pod := testutil.PersistentRootfsPod()
	rawPod, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	review := admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:    "uid",
			Object: runtime.RawExtension{Raw: rawPod},
		},
	}
	recorder := postJSON(t, NewAdmissionHandler(config.Default()), "/mutate-v1-pod", review)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var out admissionv1.AdmissionReview
	if err := json.NewDecoder(recorder.Body).Decode(&out); err != nil {
		t.Fatalf("decode admission review: %v", err)
	}
	if out.Response == nil || !out.Response.Allowed || len(out.Response.Patch) == 0 {
		t.Fatalf("expected allowed mutation with patch: %+v", out.Response)
	}
}

func postJSON(t *testing.T, handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

type fakeRootfsService struct {
	resolveReq  rootfsapi.ResolveRootfsRequest
	resolveResp rootfsapi.ResolveRootfsResponse
	resolveErr  error

	releaseReq  rootfsapi.ReleaseRootfsRequest
	releaseResp rootfsapi.ReleaseRootfsResponse
	releaseErr  error

	mountedReq  rootfsapi.MarkMountedRequest
	beginReq    rootfsapi.BeginCreateRequest
	registerReq rootfsapi.RegisterDelegateRequest
	commitReq   rootfsapi.CommitCreateRequest
}

func (f *fakeRootfsService) ResolveRootfs(_ context.Context, req rootfsapi.ResolveRootfsRequest) (rootfsapi.ResolveRootfsResponse, error) {
	f.resolveReq = req
	return f.resolveResp, f.resolveErr
}

func (f *fakeRootfsService) ReleaseRootfs(_ context.Context, req rootfsapi.ReleaseRootfsRequest) (rootfsapi.ReleaseRootfsResponse, error) {
	f.releaseReq = req
	return f.releaseResp, f.releaseErr
}

func (f *fakeRootfsService) MarkMounted(_ context.Context, req rootfsapi.MarkMountedRequest) (rootfsapi.MarkMountedResponse, error) {
	f.mountedReq = req
	return rootfsapi.MarkMountedResponse{Mounted: true}, nil
}

func (f *fakeRootfsService) BeginCreate(_ context.Context, req rootfsapi.BeginCreateRequest) (rootfsapi.BeginCreateResponse, error) {
	f.beginReq = req
	return rootfsapi.BeginCreateResponse{AttemptToken: "token"}, nil
}

func (f *fakeRootfsService) RegisterDelegate(_ context.Context, req rootfsapi.RegisterDelegateRequest) (rootfsapi.RegisterDelegateResponse, error) {
	f.registerReq = req
	return rootfsapi.RegisterDelegateResponse{Registered: true}, nil
}

func (f *fakeRootfsService) CommitCreate(_ context.Context, req rootfsapi.CommitCreateRequest) (rootfsapi.CommitCreateResponse, error) {
	f.commitReq = req
	return rootfsapi.CommitCreateResponse{Created: true}, nil
}
