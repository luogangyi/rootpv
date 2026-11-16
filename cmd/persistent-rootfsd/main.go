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
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"gitcode.com/openFuyao/rootpv/internal/crio"
	"gitcode.com/openFuyao/rootpv/internal/httpapi"
	"gitcode.com/openFuyao/rootpv/internal/node"
	"gitcode.com/openFuyao/rootpv/internal/rootfsd"
)

func main() {
	var opts options
	flag.StringVar(&opts.runtimeProvider, "runtime-provider", "containerd", "containerd or cri-o")
	flag.StringVar(&opts.crioSocket, "crio-socket", "/var/run/crio/crio.sock", "CRI-O Unix socket")
	flag.StringVar(&opts.crioDropIn, "crio-drop-in", "/host/etc/crio/crio.conf.d/99-rootpv.conf", "dedicated CRI-O handler drop-in")
	flag.StringVar(&opts.crioRestartCommand, "crio-restart-command", "", "command to restart CRI-O after handler changes")
	flag.StringVar(&opts.crioStorageHelper, "crio-storage-helper", "/usr/local/bin/rootpv-crio-storage", "storage helper executable")
	flag.StringVar(&opts.crioSourceRoot, "crio-source-root", "/var/lib/rootpv/crio-sources", "persistent source intents")
	flag.StringVar(&opts.metricsAddr, "metrics-addr", ":8080", "HTTP health and metrics listen address")
	flag.StringVar(&opts.nodeName, "node-name", os.Getenv("NODE_NAME"), "Kubernetes node name")
	flag.StringVar(&opts.kubeletRoot, "kubelet-root", "/var/lib/kubelet", "kubelet root path")
	flag.StringVar(&opts.rootfsdSocket, "rootfsd-socket", "/run/persistent-rootfsd/rootfsd.sock", "rootfsd local Unix socket")
	flag.StringVar(&opts.runtimeRoot, "runtime-root", "/run/rootpv/rootfs", "shared runtime lifecycle state root")
	flag.BoolVar(&opts.runtimeAdapterDiscovery, "runtime-adapter-discovery", true, "publish discovered runtime inventory")
	flag.BoolVar(&opts.publishNodeCapabilities, "publish-node-capabilities", true, "publish node capability and adapter readiness labels; disable for isolated manual runtime tests")
	flag.StringVar(&opts.containerdConfig, "containerd-config", "/host/etc/containerd/config.toml", "host containerd config for runtime discovery")
	flag.StringVar(&opts.hostRoot, "host-root", "/host", "host filesystem root for runtime discovery")
	flag.StringVar(&opts.runtimeAdapterRegistry, "runtime-adapter-registry", "/host/etc/rootpv/rootpv-runtime-adapters.yaml", "node-local generated runtime adapter registry")
	flag.StringVar(&opts.containerdRestartCommand, "containerd-restart-command", "", "shell command to restart or reload containerd after adapter changes")
	flag.StringVar(&opts.podNamespace, "pod-namespace", os.Getenv("POD_NAMESPACE"), "RootPV namespace containing the generated runtime catalog")
	flag.Parse()

	if err := run(opts); err != nil {
		log.Fatal(err)
	}
}

type options struct {
	runtimeProvider          string
	crioSocket               string
	crioDropIn               string
	crioRestartCommand       string
	crioStorageHelper        string
	crioSourceRoot           string
	metricsAddr              string
	nodeName                 string
	kubeletRoot              string
	rootfsdSocket            string
	runtimeRoot              string
	runtimeAdapterDiscovery  bool
	publishNodeCapabilities  bool
	containerdConfig         string
	hostRoot                 string
	runtimeAdapterRegistry   string
	containerdRestartCommand string
	podNamespace             string
}

func run(opts options) error {
	if opts.runtimeProvider != "containerd" && opts.runtimeProvider != crio.Provider {
		return fmt.Errorf("unsupported runtime provider %q", opts.runtimeProvider)
	}
	manager := rootfsd.NewManager(rootfsd.ManagerOptions{NodeName: opts.nodeName})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cleaner := rootfsd.NamespaceRecoveryCleaner{}
	reconcile := func() error {
		if err := manager.Reconcile(ctx, opts.runtimeRoot, cleaner); err != nil {
			return err
		}
		if opts.runtimeProvider == crio.Provider {
			store := crio.Storage{Client: crio.Client{Socket: opts.crioSocket}, Helper: opts.crioStorageHelper, StateRoot: opts.crioSourceRoot}
			return store.Reap(ctx, opts.runtimeRoot, rootfsd.ProcProcessChecker{}.Alive)
		}
		return nil
	}
	if err := reconcile(); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := reconcile(); err != nil {
					log.Printf("rootfs lifecycle reconciliation failed: %v", err)
					stop()
					return
				}
			}
		}
	}()

	if err := startRootfsdSocket(ctx, opts.rootfsdSocket, manager); err != nil {
		return err
	}
	go node.StartPodReconciler(ctx, opts.nodeName, opts.kubeletRoot, manager, node.CapabilityOptions{RootfsdSocket: opts.rootfsdSocket, Disabled: !opts.publishNodeCapabilities})
	startRuntimeDiscovery(ctx, opts)

	metricsSrv := &http.Server{
		Addr:              opts.metricsAddr,
		Handler:           httpapi.NewHealthHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("node-agent health server listening on %s", opts.metricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("node-agent health server error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return metricsSrv.Shutdown(shutdownCtx)
}

func startRuntimeDiscovery(ctx context.Context, opts options) {
	if !opts.runtimeAdapterDiscovery {
		return
	}
	if opts.runtimeProvider == crio.Provider {
		go node.StartCRIOInventoryPublisher(ctx, opts.nodeName, opts.crioSocket, opts.hostRoot)
	} else {
		go node.StartRuntimeInventoryPublisher(ctx, opts.nodeName, opts.containerdConfig, opts.hostRoot)
	}
	adapterOptions := node.RuntimeAdapterReconcilerOptions{
		Namespace:        opts.podNamespace,
		ContainerdConfig: opts.containerdConfig,
		HostRoot:         opts.hostRoot,
		WrapperBinary:    "/usr/local/bin/rootfs-runtime-wrapper",
		RegistryPath:     opts.runtimeAdapterRegistry,
		RestartCommand:   opts.containerdRestartCommand,
		SkipReadiness:    !opts.publishNodeCapabilities,
	}
	if opts.runtimeProvider == crio.Provider {
		adapterOptions.CRIOSocket = opts.crioSocket
		adapterOptions.CRIODropIn = opts.crioDropIn
		adapterOptions.RestartCommand = opts.crioRestartCommand
	}
	go node.StartRuntimeAdapterReconciler(ctx, opts.nodeName, adapterOptions)
}

func startRootfsdSocket(ctx context.Context, socket string, manager *rootfsd.Manager) error {
	if socket == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(socket); err != nil {
		return err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		return errors.Join(err, ln.Close(), os.Remove(socket))
	}
	srv := &http.Server{
		Handler:           httpapi.NewRootfsHandler(manager),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown rootfsd local socket server: %v", err)
		}
		if err := ln.Close(); err != nil {
			log.Printf("close rootfsd local socket: %v", err)
		}
		if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
			log.Printf("remove rootfsd local socket: %v", err)
		}
	}()
	go func() {
		log.Printf("rootfsd local socket listening on unix://%s", socket)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("rootfsd local socket error: %v", err)
		}
	}()
	return nil
}
