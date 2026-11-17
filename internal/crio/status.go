// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package crio

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// Active distinguishes a stopped container retained for logs from a task that
// can still write. CRI-O never restarts an exited container with the same ID.
func (c Client) Active(ctx context.Context, id string) (bool, error) {
	if !ValidContainerID(id) || !filepath.IsAbs(c.Socket) {
		return false, fmt.Errorf("invalid CRI-O container ID or socket")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "unix://"+c.Socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, err
	}
	defer conn.Close()
	response, err := runtimeapi.NewRuntimeServiceClient(conn).ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{ContainerId: id})
	if status.Code(err) == codes.NotFound {
		// ContainerStatus hides in-progress creates; the inspection endpoint
		// sees them before the runtime Create RPC has completed.
		_, err = c.Container(ctx, id)
		if err == ErrNotFound {
			return false, nil
		}
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	if response.Status == nil {
		return false, fmt.Errorf("CRI-O container status unavailable")
	}
	switch response.Status.State {
	case runtimeapi.ContainerState_CONTAINER_CREATED, runtimeapi.ContainerState_CONTAINER_RUNNING:
		return true, nil
	case runtimeapi.ContainerState_CONTAINER_EXITED:
		return false, nil
	default:
		return false, fmt.Errorf("CRI-O container state is unknown")
	}
}
