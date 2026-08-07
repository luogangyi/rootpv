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

package v1alpha1

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestLifecycleTypesJSONRoundTrip(t *testing.T) {
	assertLifecycleJSONRoundTrip(t, lifecycleResolveJSONCases())
	assertLifecycleJSONRoundTrip(t, lifecycleCreateJSONCases())
	assertLifecycleJSONRoundTrip(t, lifecycleReleaseJSONCases())
}

type lifecycleJSONCase struct {
	name string
	in   any
	out  any
	keys []string
}

func lifecycleResolveJSONCases() []lifecycleJSONCase {
	return []lifecycleJSONCase{
		{
			name: "resolve",
			in: ResolveRootfsRequest{
				ContainerID:   "container-1",
				Snapshotter:   "overlayfs",
				RootfsChainID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
			out:  &ResolveRootfsRequest{},
			keys: []string{"snapshotter", "rootfs_chain_id"},
		},
		{
			name: "resolved lease",
			in:   ResolveRootfsResponse{Enabled: true, LeaseID: "lease-1"},
			out:  &ResolveRootfsResponse{},
			keys: []string{"lease_id"},
		},
		{
			name: "mounted",
			in:   MarkMountedRequest{LeaseID: "lease-1", RuntimeStatePath: "/run/rootpv/rootfs/container-1/state.json"},
			out:  &MarkMountedRequest{},
			keys: []string{"lease_id", "runtime_state_path"},
		},
	}
}

func lifecycleCreateJSONCases() []lifecycleJSONCase {
	return []lifecycleJSONCase{
		{name: "begin create", in: BeginCreateRequest{LeaseID: "lease-1", ContainerID: "container-1", WrapperPID: 100, WrapperStartTime: 200}, out: &BeginCreateRequest{}, keys: []string{"lease_id", "container_id", "wrapper_pid", "wrapper_start_time"}},
		{
			name: "register delegate",
			in: RegisterDelegateRequest{
				LeaseID:           "lease-1",
				AttemptToken:      "attempt-1",
				DelegatePID:       101,
				DelegateStartTime: 201,
			},
			out:  &RegisterDelegateRequest{},
			keys: []string{"lease_id", "attempt_token", "delegate_pid", "delegate_start_time"},
		},
		{
			name: "commit create",
			in:   CommitCreateRequest{LeaseID: "lease-1", AttemptToken: "attempt-1"},
			out:  &CommitCreateRequest{},
			keys: []string{"lease_id", "attempt_token"},
		},
	}
}

func lifecycleReleaseJSONCases() []lifecycleJSONCase {
	return []lifecycleJSONCase{
		{name: "release", in: ReleaseRootfsRequest{ContainerID: "container-1", LeaseID: "lease-1"}, out: &ReleaseRootfsRequest{}, keys: []string{"container_id", "lease_id"}},
		{name: "released", in: ReleaseRootfsResponse{Released: true, Reason: "released"}, out: &ReleaseRootfsResponse{}, keys: []string{"released", "reason"}},
	}
}

func assertLifecycleJSONRoundTrip(t *testing.T, tests []lifecycleJSONCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			for _, key := range tt.keys {
				if !strings.Contains(string(data), `"`+key+`"`) {
					t.Fatalf("JSON %s does not contain key %q", data, key)
				}
			}
			if err := json.Unmarshal(data, tt.out); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			got, err := json.Marshal(tt.out)
			if err != nil {
				t.Fatalf("Marshal decoded value: %v", err)
			}
			if !sameJSONValue(t, data, got) {
				t.Fatalf("round trip mismatch:\n got %s\nwant %s", got, data)
			}
		})
	}
}

// sameJSONValue 比较 JSON 的数据语义而非文本顺序，避免 map 键顺序影响 round-trip 测试。
func sameJSONValue(t *testing.T, want, got []byte) bool {
	t.Helper()
	var wantValue, gotValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}
	return reflect.DeepEqual(wantValue, gotValue)
}

func TestLifecycleResponseTypesJSONFields(t *testing.T) {
	values := []struct {
		value any
		key   string
	}{
		{MarkMountedResponse{Mounted: true}, "mounted"},
		{BeginCreateResponse{AttemptToken: "attempt-1"}, "attempt_token"},
		{RegisterDelegateResponse{Registered: true}, "registered"},
		{CommitCreateResponse{Created: true}, "created"},
		{ReleaseRootfsResponse{Released: true}, "released"},
	}
	for _, item := range values {
		data, err := json.Marshal(item.value)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"`+item.key+`"`) {
			t.Fatalf("JSON %s does not contain key %q", data, item.key)
		}
	}
}
