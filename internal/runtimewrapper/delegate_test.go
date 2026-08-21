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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

func TestDelegateHelperRegistersBeforeExec(t *testing.T) {
	client := &helperRootfsClient{}
	executor := &orderedExecutor{registered: &client.registered}
	var notify bytes.Buffer
	identity := rootfsstate.ProcessIdentity{PID: 123, StartTime: 456}
	err := RunDelegateHelper(context.Background(), client, DelegateHelperRequest{
		LeaseID:      "lease",
		AttemptToken: "token",
		Delegate:     "/usr/bin/runc",
		Args:         []string{"create", "cid"},
		Env:          []string{"A=B"},
		Notify:       &notify,
		Ack:          bytes.NewBuffer([]byte{1}),
	}, func() (rootfsstate.ProcessIdentity, error) { return identity, nil }, executor)
	if err != nil {
		t.Fatalf("RunDelegateHelper: %v", err)
	}
	if !client.registered || !executor.called {
		t.Fatalf("registration=%v exec=%v", client.registered, executor.called)
	}
	if client.registerRequest.DelegatePID != 123 || client.registerRequest.DelegateStartTime != 456 {
		t.Fatalf("unexpected registration: %+v", client.registerRequest)
	}
	if !reflect.DeepEqual(executor.args, []string{"/usr/bin/runc", "create", "cid"}) {
		t.Fatalf("unexpected exec args: %v", executor.args)
	}
	var notified rootfsstate.ProcessIdentity
	if err := json.NewDecoder(&notify).Decode(&notified); err != nil || notified != identity {
		t.Fatalf("notified=%+v err=%v", notified, err)
	}
}

func TestProcessDelegateSupervisorPreservesExitStatus(t *testing.T) {
	script := filepath.Join(t.TempDir(), "helper.sh")
	data := []byte(`#!/bin/sh
printf '{"pid":321,"start_time":654}\n' >&3
dd bs=1 count=1 <&4 >/dev/null 2>&1
exit 23
`)
	if err := os.WriteFile(script, data, 0o755); err != nil {
		t.Fatal(err)
	}
	var registered rootfsstate.ProcessIdentity
	err := (ProcessDelegateSupervisor{Executable: script}).Run(context.Background(), DelegateRequest{
		LeaseID:      "lease",
		AttemptToken: "token",
		Delegate:     "/usr/bin/runc",
		Env:          os.Environ(),
	}, func(identity rootfsstate.ProcessIdentity) error {
		registered = identity
		return nil
	})
	if registered != (rootfsstate.ProcessIdentity{PID: 321, StartTime: 654}) {
		t.Fatalf("unexpected registered identity: %+v", registered)
	}
	type exitCoder interface{ ExitCode() int }
	exitErr, ok := err.(exitCoder)
	if !ok || exitErr.ExitCode() != 23 {
		t.Fatalf("expected exit status 23, got %T %v", err, err)
	}
}

func TestDelegateHelperDoesNotExecAfterRegistrationRejection(t *testing.T) {
	client := &helperRootfsClient{registerErr: errors.New("create_attempt_revoked")}
	executor := &orderedExecutor{registered: &client.registered}
	err := RunDelegateHelper(context.Background(), client, DelegateHelperRequest{
		LeaseID:      "lease",
		AttemptToken: "revoked",
		Delegate:     "/usr/bin/runc",
		Notify:       &bytes.Buffer{},
		Ack:          bytes.NewBuffer([]byte{1}),
	}, func() (rootfsstate.ProcessIdentity, error) {
		return rootfsstate.ProcessIdentity{PID: 123, StartTime: 456}, nil
	}, executor)
	if err == nil || executor.called {
		t.Fatalf("expected registration failure without exec, err=%v exec=%v", err, executor.called)
	}
}

type helperRootfsClient struct {
	registered      bool
	registerRequest apiv1alpha1.RegisterDelegateRequest
	registerErr     error
}

func (c *helperRootfsClient) RegisterDelegate(_ context.Context, req apiv1alpha1.RegisterDelegateRequest) (apiv1alpha1.RegisterDelegateResponse, error) {
	c.registerRequest = req
	if c.registerErr != nil {
		return apiv1alpha1.RegisterDelegateResponse{}, c.registerErr
	}
	c.registered = true
	return apiv1alpha1.RegisterDelegateResponse{Registered: true}, nil
}

func (c *helperRootfsClient) ResolveRootfs(context.Context, apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
	return apiv1alpha1.ResolveRootfsResponse{}, nil
}
func (c *helperRootfsClient) MarkMounted(context.Context, apiv1alpha1.MarkMountedRequest) (apiv1alpha1.MarkMountedResponse, error) {
	return apiv1alpha1.MarkMountedResponse{}, nil
}
func (c *helperRootfsClient) BeginCreate(context.Context, apiv1alpha1.BeginCreateRequest) (apiv1alpha1.BeginCreateResponse, error) {
	return apiv1alpha1.BeginCreateResponse{}, nil
}
func (c *helperRootfsClient) CommitCreate(context.Context, apiv1alpha1.CommitCreateRequest) (apiv1alpha1.CommitCreateResponse, error) {
	return apiv1alpha1.CommitCreateResponse{}, nil
}
func (c *helperRootfsClient) ReleaseRootfs(context.Context, apiv1alpha1.ReleaseRootfsRequest) (apiv1alpha1.ReleaseRootfsResponse, error) {
	return apiv1alpha1.ReleaseRootfsResponse{}, nil
}

type orderedExecutor struct {
	registered *bool
	called     bool
	args       []string
}

func (e *orderedExecutor) Exec(_ string, args []string, _ []string) error {
	if e.registered == nil || !*e.registered {
		return errors.New("delegate exec happened before registration")
	}
	e.called = true
	e.args = append([]string(nil), args...)
	return nil
}
