//go:build linux

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
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRootPeerPIDRejectsInvalidCredentials(t *testing.T) {
	for _, credentials := range []*unix.Ucred{nil, {Uid: 1, Pid: 7}, {Pid: 0}, {Pid: -1}} {
		if pid, err := rootPeerPID(credentials); err == nil || pid != 0 {
			t.Fatalf("accepted credentials: %#v, pid=%d, error=%v", credentials, pid, err)
		}
	}
	if pid, err := rootPeerPID(&unix.Ucred{Pid: 7}); err != nil || pid != 7 {
		t.Fatalf("root peer: pid=%d, error=%v", pid, err)
	}
}

func TestPeerPIDPreservesConnection(t *testing.T) {
	address := &net.UnixAddr{Net: "unix", Name: filepath.Join(t.TempDir(), "peer.sock")}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unix", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	pid, err := peerPID(client)
	if os.Geteuid() == 0 {
		if err != nil || pid != os.Getpid() {
			t.Fatalf("peer: pid=%d, error=%v", pid, err)
		}
	} else if err == nil {
		t.Fatal("accepted a non-root peer")
	}
	if err := server.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	var payload [1]byte
	if _, err := server.Read(payload[:]); err != nil || payload[0] != 'x' {
		t.Fatalf("connection damaged: %q %v", payload, err)
	}
	assertPeerReadDeadline(t, client)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := peerPID(client); err == nil {
		t.Fatal("accepted a closed connection")
	}
}

func assertPeerReadDeadline(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var payload [1]byte
	_, err := conn.Read(payload[:])
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("peer check broke read deadlines: %v", err)
	}
}

func TestPeerPIDRejectsNonUnixConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if _, err := peerPID(client); err == nil {
		t.Fatal("accepted a non-Unix connection")
	}
}
