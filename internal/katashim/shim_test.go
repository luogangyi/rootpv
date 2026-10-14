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

package katashim

import (
	"context"
	"reflect"
	"testing"

	task "github.com/containerd/containerd/api/runtime/task/v2"
	"google.golang.org/protobuf/types/known/emptypb"
)

type controlTask struct {
	task.TaskService
	calls []string
}

func (d *controlTask) Connect(context.Context, *task.ConnectRequest) (*task.ConnectResponse, error) {
	d.calls = append(d.calls, "connect")
	return &task.ConnectResponse{ShimPid: 17, TaskPid: 42}, nil
}
func (d *controlTask) Shutdown(context.Context, *task.ShutdownRequest) (*emptypb.Empty, error) {
	d.calls = append(d.calls, "shutdown")
	return &emptypb.Empty{}, nil
}

func TestProxyReadyBeforeKataRuntimeExists(t *testing.T) {
	delegate := &controlTask{}
	s := &Service{TaskService: delegate}
	response, err := s.Connect(context.Background(), &task.ConnectRequest{})
	if err != nil || response.ShimPid == 0 || len(delegate.calls) != 0 {
		t.Fatalf("pre-create Connect touched Kata: %#v %v %v", response, err, delegate.calls)
	}
	s.created.Store(true)
	response, err = s.Connect(context.Background(), &task.ConnectRequest{})
	if err != nil || response.TaskPid != 42 || response.ShimPid == 17 || !reflect.DeepEqual(delegate.calls, []string{"connect"}) {
		t.Fatalf("Connect routing: %#v %v %v", response, err, delegate.calls)
	}
}

func TestShutdownRetainsProxyForOtherSandboxTasks(t *testing.T) {
	delegate := &controlTask{}
	stopped := false
	s := &Service{TaskService: delegate, tasks: map[string]struct{}{"sandbox": {}, "sidecar": {}}, shutdown: func() { stopped = true }}
	if _, err := s.Shutdown(context.Background(), &task.ShutdownRequest{}); err != nil {
		t.Fatal(err)
	}
	if stopped || len(delegate.calls) != 0 {
		t.Fatal("closed a shared shim with live tasks")
	}
	s.tasks = map[string]struct{}{}
	if _, err := s.Shutdown(context.Background(), &task.ShutdownRequest{}); err != nil {
		t.Fatal(err)
	}
	if !stopped || !reflect.DeepEqual(delegate.calls, []string{"shutdown"}) {
		t.Fatalf("shim not closed after all tasks deleted: %v", delegate.calls)
	}
}

func TestDelegateBootstrapAddress(t *testing.T) {
	for _, input := range []string{"unix:///run/kata.sock\n", "{\"version\":2,\"address\":\"unix:///run/kata.sock\",\"protocol\":\"ttrpc\"}"} {
		address, err := parseAddress([]byte(input))
		if err != nil || address != "unix:///run/kata.sock" {
			t.Fatalf("%q: %q %v", input, address, err)
		}
	}
	for _, input := range []string{"", "{\"version\":2,\"address\":\"x\",\"protocol\":\"grpc\"}"} {
		if _, err := parseAddress([]byte(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
