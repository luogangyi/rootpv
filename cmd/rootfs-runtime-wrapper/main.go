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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"gitcode.com/openFuyao/rootpv/internal/runtimewrapper"
)

func main() {
	cfg, err := runtimewrapper.LoadConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rootfs-runtime-wrapper: load config: %v\n", err)
		os.Exit(1)
	}
	wrapper := runtimewrapper.NewWrapper(cfg)
	if err := wrapper.Run(context.Background(), os.Args[0], os.Args[1:], os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "rootfs-runtime-wrapper: %v\n", err)
		os.Exit(exitCode(err))
	}
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr != nil {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		if code := exitErr.ExitCode(); code >= 0 {
			return code
		}
	}
	return 1
}
