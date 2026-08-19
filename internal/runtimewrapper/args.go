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
	"fmt"
	"strings"
)

type Invocation struct {
	Command     string
	ContainerID string
	Bundle      string
	Handled     bool
}

func ParseInvocation(args []string) (Invocation, error) {
	commandIndex, ignored := invocationCommand(args)
	if ignored.Command != "" {
		return ignored, nil
	}
	if commandIndex < 0 {
		return Invocation{Handled: false}, nil
	}

	inv := Invocation{Command: args[commandIndex], Handled: true}
	if err := parseInvocationArguments(&inv, args[commandIndex+1:]); err != nil {
		if !inv.NeedsMount() && !inv.NeedsCleanup() {
			// 非创建、删除命令不改写 OCI spec 或清理 RootPV 资源；遇到新版
			// runtime 尚未识别的参数时直接透传，避免 wrapper 阻断原本合法的调用。
			return Invocation{Command: inv.Command}, nil
		}
		return Invocation{}, err
	}
	if inv.ContainerID == "" {
		if !inv.NeedsMount() && !inv.NeedsCleanup() {
			return Invocation{Command: inv.Command}, nil
		}
		return Invocation{}, fmt.Errorf("%s requires container id", inv.Command)
	}
	return inv, nil
}

// invocationCommand 定位 runtime 子命令；明确不改写的子命令会返回未处理的 Invocation。
func invocationCommand(args []string) (int, Invocation) {
	for index, arg := range args {
		if isContainerCommand(arg) {
			return index, Invocation{}
		}
		if isKnownNonMountCommand(arg) {
			return -1, Invocation{Command: arg}
		}
	}
	return -1, Invocation{}
}

// parseInvocationArguments 提取 bundle 和容器 ID，并跳过已知 runtime 全局参数。
func parseInvocationArguments(inv *Invocation, args []string) error {
	for index := 0; index < len(args); {
		next, err := consumeInvocationArgument(inv, args, index)
		if err != nil {
			return err
		}
		index = next
	}
	return nil
}

func consumeInvocationArgument(inv *Invocation, args []string, index int) (int, error) {
	arg := args[index]
	next := index + 1
	switch {
	case arg == "--bundle" || arg == "-b":
		if next >= len(args) {
			return 0, fmt.Errorf("%s requires a value", arg)
		}
		inv.Bundle = args[next]
		return next + 1, nil
	case hasPrefix(arg, "--bundle="):
		inv.Bundle = arg[len("--bundle="):]
	case hasPrefix(arg, "-b="):
		inv.Bundle = arg[len("-b="):]
	case hasPrefix(arg, "-"):
		return consumeRuntimeFlag(inv, args, index)
	default:
		if inv.ContainerID == "" {
			inv.ContainerID = arg
		}
	}
	return next, nil
}

func consumeRuntimeFlag(inv *Invocation, args []string, index int) (int, error) {
	arg := args[index]
	next := index + 1
	if flagLikelyHasValue(arg) && next < len(args) && !hasPrefix(args[next], "-") {
		return next + 1, nil
	}
	if flagLikelyHasNoValue(arg) || strings.Contains(arg, "=") {
		return next, nil
	}
	if inv.ContainerID == "" && next < len(args) && !hasPrefix(args[next], "-") {
		return 0, fmt.Errorf("unsupported runtime flag before container id: %s", arg)
	}
	return next, nil
}

func (i Invocation) NeedsMount() bool {
	return i.Handled && (i.Command == "create" || i.Command == "run")
}

func (i Invocation) NeedsCleanup() bool {
	return i.Handled && i.Command == "delete"
}

func isKnownNonMountCommand(command string) bool {
	switch command {
	case "list", "features":
		return true
	default:
		return false
	}
}

func isContainerCommand(command string) bool {
	switch command {
	case "create", "run", "delete", "state", "start", "kill", "pause", "resume", "ps", "events", "exec", "checkpoint", "update":
		return true
	default:
		return false
	}
}

func flagLikelyHasValue(flag string) bool {
	switch flag {
	case "--root", "--log", "--log-format", "--systemd-cgroup", "--rootless", "--console-socket", "--pid-file", "--process", "--preserve-fds",
		"--signal", "-s", "--env", "--cwd", "--user", "-u", "--process-label", "--apparmor", "--cap", "--additional-gids":
		return true
	default:
		return false
	}
}

func flagLikelyHasNoValue(flag string) bool {
	switch flag {
	case "--all", "-a", "--detach", "-d", "--force", "--no-pivot", "--no-new-keyring", "--no-subreaper",
		"--tty", "-t", "--interactive", "-i", "--no-new-privs":
		return true
	default:
		return false
	}
}

func hasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}
