//go:build linux

// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package crio

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// PeerIdentity verifies activation against the running daemon, not /config:
// some CRI-O versions reload the HTTP config copy but leave OCI routing stale.
func (c Client) PeerIdentity(ctx context.Context) (string, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		return "", fmt.Errorf("CRI-O Unix connection required")
	}
	pid, err := peerPID(conn)
	if err != nil {
		return "", err
	}
	start, err := processStartTime(pid)
	if err != nil {
		return "", err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d:%s", strings.TrimSpace(string(boot)), pid, start), nil
}

func peerPID(conn *net.UnixConn) (int, error) {
	// File returns an owned duplicate. This connection is only used for
	// identity inspection, with no concurrent I/O or deadline users.
	file, err := conn.File()
	if err != nil {
		return 0, err
	}
	defer file.Close()
	credentials, err := unix.GetsockoptUcred(int(file.Fd()), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	if credentials == nil || credentials.Pid <= 0 || credentials.Uid != 0 {
		return 0, fmt.Errorf("CRI-O peer PID unavailable; host PID namespace required")
	}
	return int(credentials.Pid), nil
}

func processStartTime(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", fmt.Errorf("invalid CRI-O process identity")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) <= 19 {
		return "", fmt.Errorf("incomplete CRI-O process identity")
	}
	return fields[19], nil
}
