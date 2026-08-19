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

import "testing"

func TestParseCreateInvocationWithBundle(t *testing.T) {
	inv, err := ParseInvocation([]string{"--root", "/run/runc", "create", "--bundle", "/tmp/bundle", "container-1"})
	if err != nil {
		t.Fatalf("ParseInvocation: %v", err)
	}
	if inv.Command != "create" || inv.ContainerID != "container-1" || inv.Bundle != "/tmp/bundle" {
		t.Fatalf("unexpected invocation: %+v", inv)
	}
	if !inv.NeedsMount() || inv.NeedsCleanup() {
		t.Fatalf("unexpected command classification: %+v", inv)
	}
}

func TestParseRunInvocationWithShortBundle(t *testing.T) {
	inv, err := ParseInvocation([]string{"run", "-b", "/tmp/bundle", "container-1"})
	if err != nil {
		t.Fatalf("ParseInvocation: %v", err)
	}
	if inv.Command != "run" || inv.ContainerID != "container-1" || inv.Bundle != "/tmp/bundle" {
		t.Fatalf("unexpected invocation: %+v", inv)
	}
}

func TestParseDeleteInvocationDoesNotRequireBundle(t *testing.T) {
	inv, err := ParseInvocation([]string{"delete", "container-1"})
	if err != nil {
		t.Fatalf("ParseInvocation: %v", err)
	}
	if inv.Command != "delete" || inv.ContainerID != "container-1" {
		t.Fatalf("unexpected invocation: %+v", inv)
	}
	if inv.NeedsMount() || !inv.NeedsCleanup() {
		t.Fatalf("unexpected command classification: %+v", inv)
	}
}

func TestParseDeleteInvocationWithForce(t *testing.T) {
	inv, err := ParseInvocation([]string{"delete", "--force", "container-1"})
	if err != nil {
		t.Fatalf("ParseInvocation: %v", err)
	}
	if inv.Command != "delete" || inv.ContainerID != "container-1" || !inv.NeedsCleanup() {
		t.Fatalf("unexpected invocation: %+v", inv)
	}
}

func TestParseKillInvocationWithAll(t *testing.T) {
	inv, err := ParseInvocation([]string{"kill", "--all", "container-1", "TERM"})
	if err != nil {
		t.Fatalf("ParseInvocation: %v", err)
	}
	if inv.Command != "kill" || inv.ContainerID != "container-1" {
		t.Fatalf("unexpected invocation: %+v", inv)
	}
}

func TestParseExecInvocationWithTerminalFlag(t *testing.T) {
	inv, err := ParseInvocation([]string{"exec", "-t", "container-1", "/bin/sh"})
	if err != nil {
		t.Fatalf("ParseInvocation: %v", err)
	}
	if !inv.Handled || inv.ContainerID != "container-1" {
		t.Fatalf("terminal exec should retain container context: %+v", inv)
	}
}

func TestParseKillInvocationWithSignalFlag(t *testing.T) {
	inv, err := ParseInvocation([]string{"kill", "-s", "SIGTERM", "container-1"})
	if err != nil {
		t.Fatalf("ParseInvocation: %v", err)
	}
	if !inv.Handled || inv.ContainerID != "container-1" {
		t.Fatalf("signal flag should retain container context: %+v", inv)
	}
}

func TestParseDoesNotTreatDetachAsValueFlag(t *testing.T) {
	inv, err := ParseInvocation([]string{"run", "--detach", "-b", "/tmp/bundle", "container-1"})
	if err != nil {
		t.Fatalf("ParseInvocation: %v", err)
	}
	if inv.ContainerID != "container-1" || inv.Bundle != "/tmp/bundle" {
		t.Fatalf("unexpected invocation: %+v", inv)
	}
}

func TestParseFailsSafeForUnknownFlagValueBeforeContainerID(t *testing.T) {
	_, err := ParseInvocation([]string{"run", "--new-runc-flag", "flag-value", "-b", "/tmp/bundle", "container-1"})
	if err == nil {
		t.Fatalf("expected fail-safe error for unknown flag value before container id")
	}
}

func TestParseDelegatesNonLifecycleCommandWithUnknownFlag(t *testing.T) {
	inv, err := ParseInvocation([]string{"exec", "--future-runc-flag", "flag-value", "container-1", "/bin/sh"})
	if err != nil {
		t.Fatalf("ParseInvocation: %v", err)
	}
	if inv.Handled {
		t.Fatalf("non-lifecycle command with unknown flag must be delegated: %+v", inv)
	}
}

func TestParseStateInvocationUsesContainerDelegation(t *testing.T) {
	inv, err := ParseInvocation([]string{"state", "container-1"})
	if err != nil {
		t.Fatalf("ParseInvocation: %v", err)
	}
	if !inv.Handled || inv.ContainerID != "container-1" {
		t.Fatalf("state should retain container context: %+v", inv)
	}
}
