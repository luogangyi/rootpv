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
	"fmt"
	"golang.org/x/sys/unix"
	"net"
)

// peerPID reads the kernel credentials of the node-local listening socket, without
// requiring a Connect RPC (runtime-rs rejects that RPC before sandbox Create).
func peerPID(conn net.Conn) (int, error) {
	socket, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("Kata shim requires a Unix socket")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *unix.Ucred
	var readErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, readErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if readErr != nil {
		return 0, readErr
	}
	if credentials.Uid != 0 || credentials.Pid <= 0 {
		return 0, fmt.Errorf("Kata shim must have a root-owned socket peer")
	}
	return int(credentials.Pid), nil
}
