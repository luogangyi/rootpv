// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package crio

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type statusServer struct {
	runtimeapi.UnimplementedRuntimeServiceServer
}

func (statusServer) ContainerStatus(_ context.Context, req *runtimeapi.ContainerStatusRequest) (*runtimeapi.ContainerStatusResponse, error) {
	state := runtimeapi.ContainerState_CONTAINER_RUNNING
	if req.ContainerId[0] == 'e' {
		state = runtimeapi.ContainerState_CONTAINER_EXITED
	}
	return &runtimeapi.ContainerStatusResponse{Status: &runtimeapi.ContainerStatus{State: state}}, nil
}

// An exited CRI record must not retain its write lease while kubelet keeps logs.
func TestCRIActive(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cri.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	runtimeapi.RegisterRuntimeServiceServer(server, &statusServer{})
	t.Cleanup(server.Stop)
	go server.Serve(listener)
	for _, id := range []string{strings.Repeat("a", 64), strings.Repeat("e", 64)} {
		active, err := (Client{Socket: socket}).Active(context.Background(), id)
		if err != nil || active != (id[0] == 'a') {
			t.Fatalf("active for %c = %v, %v", id[0], active, err)
		}
	}
}
