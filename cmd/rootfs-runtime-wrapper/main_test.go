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
	"errors"
	"os/exec"
	"testing"
)

func TestExitCodePreservesDelegateStatus(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 37").Run()
	if err == nil {
		t.Fatal("expected command failure")
	}
	if got := exitCode(err); got != 37 {
		t.Fatalf("exitCode=%d want=37", got)
	}
	if got := exitCode(errors.New("ordinary failure")); got != 1 {
		t.Fatalf("ordinary exitCode=%d want=1", got)
	}
}
