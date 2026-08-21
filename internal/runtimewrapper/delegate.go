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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

const delegateHelperCommand = "--rootpv-delegate-helper"

type DelegateRequest struct {
	LeaseID      string
	AttemptToken string
	Delegate     string
	Args         []string
	Env          []string
}

func CurrentProcessIdentity() (rootfsstate.ProcessIdentity, error) {
	return ProcessIdentityFor(os.Getpid())
}

func ProcessIdentityFor(pid int) (rootfsstate.ProcessIdentity, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return rootfsstate.ProcessIdentity{}, err
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 {
		return rootfsstate.ProcessIdentity{}, fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(string(data[closeParen+1:]))
	if len(fields) <= 19 {
		return rootfsstate.ProcessIdentity{}, fmt.Errorf("proc stat has %d fields after comm", len(fields))
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return rootfsstate.ProcessIdentity{}, fmt.Errorf("parse process start time: %w", err)
	}
	return rootfsstate.ProcessIdentity{PID: pid, StartTime: startTime}, nil
}

func CurrentMountNamespaceInode() (uint64, error) {
	info, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("mount namespace stat type is unsupported")
	}
	return stat.Ino, nil
}

type DelegateSupervisor interface {
	Run(context.Context, DelegateRequest, func(rootfsstate.ProcessIdentity) error) error
}

type CommandRunner interface {
	Run(context.Context, string, []string, []string) error
}

type ExecCommandRunner struct {
	Stdout io.Writer
	Stderr io.Writer
}

type RuntimeCleaner interface {
	CleanupPartial(context.Context, string, string, []string, []string) error
}

type ExecRuntimeCleaner struct{}

// CleanupPartial 在 delegate 已被启动但创建未提交时，主动删除可能残留的 runtime 状态。
func (ExecRuntimeCleaner) CleanupPartial(ctx context.Context, delegate, containerID string, createArgs, env []string) error {
	globalArgs := runtimeGlobalArgs(createArgs)
	stateArgs := append(append([]string{}, globalArgs...), "state", containerID)
	state := exec.CommandContext(ctx, delegate, stateArgs...)
	state.Env = env
	output, err := state.CombinedOutput()
	if err != nil {
		message := strings.ToLower(string(output))
		if strings.Contains(message, "does not exist") || strings.Contains(message, "not found") {
			return nil
		}
		return fmt.Errorf("inspect partial runtime state: %w: %s", err, strings.TrimSpace(string(output)))
	}
	deleteArgs := append(append([]string{}, globalArgs...), "delete", "--force", containerID)
	remove := exec.CommandContext(ctx, delegate, deleteArgs...)
	remove.Env = env
	if output, err := remove.CombinedOutput(); err != nil {
		return fmt.Errorf("delete partial runtime: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runtimeGlobalArgs(args []string) []string {
	for i, arg := range args {
		if arg == "create" || arg == "run" {
			return append([]string(nil), args[:i]...)
		}
	}
	return nil
}

func (r ExecCommandRunner) Run(ctx context.Context, path string, args, env []string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	return cmd.Run()
}

type ProcessDelegateSupervisor struct {
	Executable string
	Stdout     io.Writer
	Stderr     io.Writer
}

// Run 通过两条管道实现“登记后再 exec”的握手：helper 先报告自身身份，
// 父进程持久化状态并发送 ACK，只有收到 ACK 后 helper 才会执行真实 runtime。
func (s ProcessDelegateSupervisor) Run(ctx context.Context, req DelegateRequest, registered func(rootfsstate.ProcessIdentity) error) error {
	if req.LeaseID == "" || req.AttemptToken == "" || req.Delegate == "" || registered == nil {
		return fmt.Errorf("delegate supervisor request is incomplete")
	}
	cmd, pipes, err := s.startDelegateHelper(ctx, req)
	if err != nil {
		return err
	}
	stopForwarding := forwardDelegateSignals(cmd)
	defer stopForwarding()
	result := awaitDelegateRegistration(ctx, cmd, pipes, registered)
	if closeErr := pipes.close(); closeErr != nil {
		return errors.Join(result, closeErr)
	}
	return result
}

type delegatePipes struct {
	notifyReader *os.File
	ackWriter    *os.File
}

func (p *delegatePipes) close() error {
	return errors.Join(p.closeNotify(), p.closeAck())
}

func (p *delegatePipes) closeNotify() error {
	if p.notifyReader == nil {
		return nil
	}
	err := p.notifyReader.Close()
	p.notifyReader = nil
	return err
}

func (p *delegatePipes) closeAck() error {
	if p.ackWriter == nil {
		return nil
	}
	err := p.ackWriter.Close()
	p.ackWriter = nil
	return err
}

func (s ProcessDelegateSupervisor) startDelegateHelper(ctx context.Context, req DelegateRequest) (*exec.Cmd, *delegatePipes, error) {
	executable := s.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, nil, fmt.Errorf("locate wrapper executable: %w", err)
		}
	}
	notifyReader, notifyWriter, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create delegate notification pipe: %w", err)
	}
	ackReader, ackWriter, err := os.Pipe()
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("create delegate ACK pipe: %w", err), closeDelegateFiles(notifyReader, notifyWriter))
	}
	helperArgs := []string{delegateHelperCommand, req.LeaseID, req.AttemptToken, req.Delegate}
	helperArgs = append(helperArgs, req.Args...)
	cmd := exec.CommandContext(ctx, executable, helperArgs...)
	cmd.Env = req.Env
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	// ExtraFiles 会在子进程中映射为 fd 3/4，分别用于身份通知和 ACK 等待。
	cmd.ExtraFiles = []*os.File{notifyWriter, ackReader}
	if err := cmd.Start(); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("start delegate helper: %w", err), closeDelegateFiles(notifyReader, notifyWriter, ackReader, ackWriter))
	}
	if err := closeDelegateFiles(notifyWriter, ackReader); err != nil {
		return nil, nil, errors.Join(stopDelegate(cmd, err), closeDelegateFiles(notifyReader, ackWriter))
	}
	return cmd, &delegatePipes{notifyReader: notifyReader, ackWriter: ackWriter}, nil
}

func forwardDelegateSignals(cmd *exec.Cmd) func() {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	stopForwarding := make(chan struct{})
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		forwardDelegateSignalLoop(cmd, signals, stopForwarding)
	}()
	return func() {
		signal.Stop(signals)
		close(stopForwarding)
		<-forwardDone
	}
}

func forwardDelegateSignalLoop(cmd *exec.Cmd, signals <-chan os.Signal, stop <-chan struct{}) {
	for {
		select {
		case signal := <-signals:
			if err := forwardDelegateSignal(cmd, signal); err != nil {
				continue
			}
		case <-stop:
			return
		}
	}
}

func forwardDelegateSignal(cmd *exec.Cmd, signal os.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(signal)
}

type identityResult struct {
	identity rootfsstate.ProcessIdentity
	err      error
}

func awaitDelegateRegistration(ctx context.Context, cmd *exec.Cmd, pipes *delegatePipes, registered func(rootfsstate.ProcessIdentity) error) error {
	identityCh := make(chan identityResult, 1)
	go func() {
		identityCh <- decodeDelegateIdentity(pipes.notifyReader)
	}()
	select {
	case result := <-identityCh:
		if result.err != nil {
			waitErr := cmd.Wait()
			if waitErr != nil {
				return waitErr
			}
			return fmt.Errorf("read delegate registration notification: %w", result.err)
		}
		if !result.identity.Valid() {
			return stopDelegate(cmd, fmt.Errorf("delegate helper reported invalid process identity"))
		}
		if err := registered(result.identity); err != nil {
			return stopDelegate(cmd, fmt.Errorf("persist delegate registration: %w", err))
		}
		if _, err := pipes.ackWriter.Write([]byte{1}); err != nil {
			return stopDelegate(cmd, fmt.Errorf("acknowledge delegate registration: %w", err))
		}
		if err := pipes.closeAck(); err != nil {
			return stopDelegate(cmd, err)
		}
		return cmd.Wait()
	case <-ctx.Done():
		return stopDelegate(cmd, ctx.Err())
	}
}

func decodeDelegateIdentity(reader io.Reader) identityResult {
	var identity rootfsstate.ProcessIdentity
	if err := json.NewDecoder(reader).Decode(&identity); err != nil {
		return identityResult{err: err}
	}
	return identityResult{identity: identity}
}

// closeDelegateFiles 关闭父进程不再使用的管道端，并合并多个关闭失败，保证子进程可见 EOF。
func closeDelegateFiles(files ...*os.File) error {
	var closeErr error
	for _, file := range files {
		if err := file.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return closeErr
}

// stopDelegate 在握手失败或上下文取消时终止 helper 并等待其退出，保留原始失败和回收失败。
func stopDelegate(cmd *exec.Cmd, cause error) error {
	var stopErr error
	if cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil {
			stopErr = errors.Join(stopErr, err)
		}
	}
	if err := cmd.Wait(); err != nil {
		stopErr = errors.Join(stopErr, err)
	}
	return errors.Join(cause, stopErr)
}

type DelegateHelperRequest struct {
	LeaseID      string
	AttemptToken string
	Delegate     string
	Args         []string
	Env          []string
	Notify       io.Writer
	Ack          io.Reader
}

// Executor 抽象进程映像替换操作，使 delegate helper 可在测试中注入执行器，
// 同时避免基础委托协议依赖完整的 Wrapper 实现。
type Executor interface {
	Exec(path string, args []string, env []string) error
}

func RunDelegateHelper(ctx context.Context, rootfs RootfsClient, req DelegateHelperRequest, identity func() (rootfsstate.ProcessIdentity, error), executor Executor) error {
	if rootfs == nil || identity == nil || executor == nil {
		return fmt.Errorf("delegate helper dependencies are required")
	}
	if req.LeaseID == "" || req.AttemptToken == "" || req.Delegate == "" || req.Notify == nil || req.Ack == nil {
		return fmt.Errorf("delegate helper request is incomplete")
	}
	process, err := identity()
	if err != nil {
		return fmt.Errorf("read delegate process identity: %w", err)
	}
	if !process.Valid() {
		return fmt.Errorf("delegate process identity is invalid")
	}
	// rootfsd 先校验 lease 与 attempt token，避免无关进程伪造 delegate 身份。
	registered, err := rootfs.RegisterDelegate(ctx, apiv1alpha1.RegisterDelegateRequest{
		LeaseID:           req.LeaseID,
		AttemptToken:      req.AttemptToken,
		DelegatePID:       process.PID,
		DelegateStartTime: process.StartTime,
	})
	if err != nil {
		return err
	}
	if !registered.Registered {
		return fmt.Errorf("rootfsd did not acknowledge delegate registration")
	}
	if err := json.NewEncoder(req.Notify).Encode(process); err != nil {
		return fmt.Errorf("notify parent of delegate registration: %w", err)
	}
	var ack [1]byte
	if _, err := io.ReadFull(req.Ack, ack[:]); err != nil {
		return fmt.Errorf("wait for parent delegate ACK: %w", err)
	}
	if ack[0] != 1 {
		return fmt.Errorf("parent rejected delegate registration")
	}
	execArgs := append([]string{req.Delegate}, req.Args...)
	return executor.Exec(req.Delegate, execArgs, req.Env)
}

func parseDelegateHelper(args []string, env []string) (DelegateHelperRequest, bool, error) {
	if len(args) == 0 || args[0] != delegateHelperCommand {
		return DelegateHelperRequest{}, false, nil
	}
	if len(args) < 4 {
		return DelegateHelperRequest{}, true, fmt.Errorf("delegate helper requires lease ID, attempt token, and delegate binary")
	}
	return DelegateHelperRequest{
		LeaseID:      args[1],
		AttemptToken: args[2],
		Delegate:     args[3],
		Args:         append([]string(nil), args[4:]...),
		Env:          append([]string(nil), env...),
	}, true, nil
}
