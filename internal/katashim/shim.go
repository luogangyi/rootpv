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

// Package katashim proxies the containerd task API to the configured Kata QEMU
// shim. Each sandbox pins its delegate, independent of later registry changes.
package katashim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	task "github.com/containerd/containerd/api/runtime/task/v2"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/runtime/v2/shim"
	"github.com/containerd/ttrpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"gitcode.com/openFuyao/rootpv/internal/atomicfile"
	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
	"gitcode.com/openFuyao/rootpv/internal/runtimewrapper"
)

const bootstrapFile = "rootpv-kata-bootstrap.json"

type bootstrap struct {
	AdapterID string
	Adapter   runtimewrapper.ResolvedRuntimeAdapter
	Address   string
	Args      []string
}

type Service struct {
	task.TaskService
	cfg      runtimewrapper.Config
	pinned   bootstrap
	id       string
	shutdown func()
	client   *ttrpc.Client
	rootfs   *RootfsLifecycle
	created  atomic.Bool
	createMu sync.Mutex
	tasks    map[string]struct{}
}

func New(ctx context.Context, id string, _ shim.Publisher, shutdown func()) (shim.Shim, error) {
	cfg, err := runtimewrapper.LoadConfigFromEnv()
	if err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg, id: id, shutdown: shutdown, tasks: map[string]struct{}{}}
	// Only the serving process connects here. start/delete actions perform their
	// own routing and must not fail merely because a previous shim has exited.
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] != "start" && os.Args[len(os.Args)-1] != "delete" {
		if err := s.connect(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Service) connect(ctx context.Context) error {
	data, err := readBootstrap()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &s.pinned); err != nil {
		return err
	}
	conn, err := shim.AnonDialer(s.pinned.Address, 5*time.Second)
	if err != nil {
		return err
	}
	_, err = peerPID(conn)
	if err != nil {
		conn.Close()
		return err
	}
	s.client = ttrpc.NewClient(conn, ttrpc.WithOnClose(s.shutdown))
	s.TaskService = task.NewTaskClient(s.client)
	s.rootfs = NewRootfsLifecycle(s.cfg, s.pinned.AdapterID, s.pinned.Adapter, 0)
	return nil
}

func (s *Service) StartShim(ctx context.Context, opts shim.StartOpts) (_ string, retErr error) {
	spec, err := runtimewrapper.LoadSpec(".")
	if err != nil {
		return "", err
	}
	id, adapter, err := s.resolveAdapter()
	if err != nil {
		return "", err
	}
	group := opts.ID
	if sandbox := spec.Annotations["io.kubernetes.cri.sandbox-id"]; sandbox != "" {
		group = sandbox
	}
	address, err := shim.SocketAddress(ctx, opts.Address, group+"-rootpv-kata")
	if err != nil {
		return "", err
	}
	if shim.CanConnect(address) {
		return address, shim.WriteAddress("address", address)
	}
	socket, err := shim.NewSocket(address)
	if err != nil {
		return "", err
	}
	socket.SetUnlinkOnClose(false)
	defer socket.Close()
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, shim.RemoveSocket(address))
		}
	}()
	// Preserve Kata's original options protobuf on stdin and all vendor options
	// on Create. BinaryName is never used for this task-shim runtime.
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
	if err != nil {
		return "", err
	}
	args, err := startArgs(ctx, opts)
	if err != nil {
		return "", err
	}
	var delegateIdentity rootfsstate.ProcessIdentity
	s.pinned, delegateIdentity, err = startDelegate(ctx, id, adapter, args, input)
	if err != nil {
		return "", err
	}
	defer func() {
		if retErr != nil {
			retErr = cleanupFailedStart(ctx, s.pinned, delegateIdentity, retErr)
		}
	}()
	if err := writeBootstrap(s.pinned); err != nil {
		return "", err
	}
	if err := startProxy(ctx, socket, address, opts.ID, args); err != nil {
		return "", err
	}
	return address, nil
}

func writeBootstrap(pinned bootstrap) error {
	data, err := json.Marshal(pinned)
	if err != nil {
		return err
	}
	return atomicfile.Write(bootstrapFile+".tmp", bootstrapFile, data, 0o600)
}

func (s *Service) resolveAdapter() (string, runtimewrapper.ResolvedRuntimeAdapter, error) {
	registry, err := runtimewrapper.LoadAdapterRegistry(s.cfg.RuntimeAdapterRegistry, runtimewrapper.RegistryOptions{WrapperBinary: os.Args[0]})
	if err != nil {
		return "", runtimewrapper.ResolvedRuntimeAdapter{}, err
	}
	id := strings.TrimPrefix(filepath.Base(os.Args[0]), "containerd-shim-rootpv-")
	adapter, ok := registry.Adapters[id]
	if !ok || !config.IsKataQemuRuntimeType(adapter.RuntimeType) {
		return "", runtimewrapper.ResolvedRuntimeAdapter{}, fmt.Errorf("kata runtime adapter not found for %s", os.Args[0])
	}
	return id, adapter, nil
}

func startArgs(ctx context.Context, opts shim.StartOpts) ([]string, error) {
	ns, err := namespaces.NamespaceRequired(ctx)
	if err != nil {
		return nil, err
	}
	publishBinary := opts.ContainerdBinary
	if publishBinary == "" {
		for i, arg := range os.Args {
			if arg == "-publish-binary" && i+1 < len(os.Args) {
				publishBinary = os.Args[i+1]
			}
		}
	}
	if publishBinary == "" {
		publishBinary, err = exec.LookPath("containerd")
		if err != nil {
			return nil, err
		}
	}
	args := []string{"-namespace", ns, "-id", opts.ID, "-address", opts.Address, "-publish-binary", publishBinary}
	if opts.Debug {
		args = append(args, "-debug")
	}
	return args, nil
}

func startDelegate(ctx context.Context, id string, adapter runtimewrapper.ResolvedRuntimeAdapter, args []string, input []byte) (bootstrap, rootfsstate.ProcessIdentity, error) {
	cmd := exec.CommandContext(ctx, adapter.DelegateBinary, append(args, "start")...)
	cmd.Stdin, cmd.Stderr = bytes.NewReader(input), os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return bootstrap{}, rootfsstate.ProcessIdentity{}, fmt.Errorf("start Kata delegate: %w", err)
	}
	delegateAddress, err := parseAddress(output)
	if err != nil {
		return bootstrap{}, rootfsstate.ProcessIdentity{}, err
	}
	delegateIdentity, err := startedDelegateIdentity(adapter.DelegateBinary)
	if err != nil {
		return bootstrap{}, rootfsstate.ProcessIdentity{}, err
	}
	return bootstrap{AdapterID: id, Adapter: adapter, Address: delegateAddress, Args: args}, delegateIdentity, nil
}

func cleanupFailedStart(ctx context.Context, pinned bootstrap, identity rootfsstate.ProcessIdentity, cause error) error {
	cleanup := exec.CommandContext(context.WithoutCancel(ctx), pinned.Adapter.DelegateBinary, append(pinned.Args, "delete")...)
	if output, err := cleanup.CombinedOutput(); err != nil {
		cause = errors.Join(cause, fmt.Errorf("cleanup failed Kata start: %w: %s", err, output))
	}
	// No task Create can reach Kata before StartShim returns. If the vendor
	// cleanup leaves its idle shim alive, stop only the pinned PID/starttime.
	if current, err := runtimewrapper.ProcessIdentityFor(identity.PID); err == nil && current == identity {
		cause = errors.Join(cause, syscall.Kill(identity.PID, syscall.SIGKILL))
	}
	return cause
}

func startProxy(ctx context.Context, socket *net.UnixListener, address, id string, args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	file, err := socket.File()
	if err != nil {
		return err
	}
	defer file.Close()
	child := exec.Command(self, args...)
	child.Env = os.Environ()
	child.Stderr = os.Stderr
	child.ExtraFiles = []*os.File{file}
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		return err
	}
	go child.Wait()
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := waitProxy(readyCtx, address, id); err != nil {
		return stopFailedProxy(child.Process, err)
	}
	if err := shim.WriteAddress("address", address); err != nil {
		return stopFailedProxy(child.Process, err)
	}
	return nil
}

func stopFailedProxy(process *os.Process, cause error) error {
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return errors.Join(cause, fmt.Errorf("stop failed RootPV proxy: %w", err))
	}
	return cause
}

func parseAddress(data []byte) (string, error) {
	address := strings.TrimSpace(string(data))
	if strings.HasPrefix(address, "{") {
		var params shim.BootstrapParams
		if err := json.Unmarshal(data, &params); err != nil {
			return "", err
		}
		if params.Protocol != "ttrpc" {
			return "", fmt.Errorf("unsupported Kata shim protocol %q", params.Protocol)
		}
		address = params.Address
	}
	if address == "" {
		return "", fmt.Errorf("Kata shim returned an empty address")
	}
	return address, nil
}

func (s *Service) Create(ctx context.Context, req *task.CreateTaskRequest) (*task.CreateTaskResponse, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if s.rootfs.DelegatePID == 0 && s.created.Load() {
		connected, err := s.TaskService.Connect(ctx, &task.ConnectRequest{ID: s.id})
		if err != nil {
			return nil, err
		}
		s.rootfs.DelegatePID = int(connected.ShimPid)
	}
	response, err := s.rootfs.Create(ctx, s.TaskService, req)
	if err == nil {
		s.created.Store(true)
		s.tasks[req.ID] = struct{}{}
	}
	return response, err
}

func (s *Service) Delete(ctx context.Context, req *task.DeleteRequest) (*task.DeleteResponse, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	response, err := s.rootfs.Delete(ctx, s.TaskService, req)
	if err == nil && req.ExecID == "" {
		delete(s.tasks, req.ID)
	}
	return response, err
}

func (s *Service) Connect(ctx context.Context, req *task.ConnectRequest) (*task.ConnectResponse, error) {
	if !s.created.Load() {
		return &task.ConnectResponse{ShimPid: uint32(os.Getpid())}, nil
	}
	response, err := s.TaskService.Connect(ctx, req)
	if err == nil {
		response.ShimPid = uint32(os.Getpid())
	}
	return response, err
}

func (s *Service) Shutdown(ctx context.Context, req *task.ShutdownRequest) (*emptypb.Empty, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if len(s.tasks) > 0 {
		return &emptypb.Empty{}, nil
	}
	response, err := s.TaskService.Shutdown(ctx, req)
	if err == nil {
		s.shutdown()
	}
	return response, err
}

// Cleanup is invoked after the proxy dies. Kata's own cleanup must complete
// before RootPV can release a write-layer lease.
func (s *Service) Cleanup(ctx context.Context) (*task.DeleteResponse, error) {
	data, err := readBootstrap()
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.pinned); err != nil {
		return nil, err
	}
	ns, err := namespaces.NamespaceRequired(ctx)
	if err != nil {
		return nil, err
	}
	args := append([]string(nil), s.pinned.Args...)
	if len(args) == 0 {
		args = []string{"-namespace", ns, "-id", s.id}
	}
	for i, arg := range args {
		if arg == "-id" && i+1 < len(args) {
			args[i+1] = s.id
		}
	}
	cmd := exec.CommandContext(ctx, s.pinned.Adapter.DelegateBinary, append(args, "delete")...)
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("Kata delegate cleanup: %w", err)
	}
	var response task.DeleteResponse
	if err := proto.Unmarshal(output, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func isNotFound(err error) bool { return status.Code(err) == codes.NotFound }

// A listening inherited FD alone does not prove the child initialized. Wait
// for an actual task RPC before containerd can begin creating the sandbox.
func waitProxy(ctx context.Context, address, id string) error {
	conn, err := shim.AnonDialer(address, time.Second)
	if err != nil {
		return err
	}
	client := ttrpc.NewClient(conn)
	defer client.Close()
	_, err = task.NewTaskClient(client).Connect(ctx, &task.ConnectRequest{ID: id})
	return err
}

func readBootstrap() ([]byte, error) {
	data, err := os.ReadFile(bootstrapFile)
	if err == nil || !os.IsNotExist(err) {
		return data, err
	}
	spec, specErr := runtimewrapper.LoadSpec(".")
	if specErr != nil {
		return nil, err
	}
	sandbox := spec.Annotations["io.kubernetes.cri.sandbox-id"]
	if sandbox == "" || filepath.Base(sandbox) != sandbox {
		return nil, err
	}
	return os.ReadFile(filepath.Join("..", sandbox, bootstrapFile))
}

// Kata writes the serving process PID to shim.pid before its start command
// returns. Socket peer credentials identify the short-lived starter when the
// listening FD is inherited, so they cannot identify the live shim.
func startedDelegateIdentity(binary string) (rootfsstate.ProcessIdentity, error) {
	data, err := os.ReadFile("shim.pid")
	if err != nil {
		return rootfsstate.ProcessIdentity{}, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return rootfsstate.ProcessIdentity{}, err
	}
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil || target != binary {
		return rootfsstate.ProcessIdentity{}, fmt.Errorf("Kata shim PID does not match its configured executable")
	}
	return runtimewrapper.ProcessIdentityFor(pid)
}
