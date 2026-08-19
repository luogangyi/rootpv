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
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/httpapi"
	"gitcode.com/openFuyao/rootpv/internal/node"
	"gitcode.com/openFuyao/rootpv/internal/runtimecatalog"
)

func main() {
	var opts options
	flag.StringVar(&opts.addr, "addr", ":9443", "HTTPS webhook listen address")
	flag.StringVar(&opts.metricsAddr, "metrics-addr", ":8080", "HTTP health and metrics listen address")
	flag.StringVar(&opts.certFile, "tls-cert-file", "/tls/tls.crt", "TLS certificate file")
	flag.StringVar(&opts.keyFile, "tls-private-key-file", "/tls/tls.key", "TLS private key file")
	flag.StringVar(&opts.runtimeClassMode, "runtime-class-mode", string(config.RuntimeClassModeInject), "RuntimeClass mutation mode: inject or preserve")
	flag.BoolVar(&opts.runtimeAdapterCutover, "runtime-adapter-cutover", false, "Use Runtime Adapter admission configuration")
	flag.StringVar(&opts.runtimeAdaptersConfig, "runtime-adapters-config", "", "Runtime Adapter configuration file")
	flag.BoolVar(&opts.runtimeAdapterDiscovery, "runtime-adapter-discovery", true, "Discover runtime adapters and generate the catalog ConfigMap")
	flag.Parse()

	if err := run(opts); err != nil {
		log.Fatal(err)
	}
}

type options struct {
	addr                    string
	metricsAddr             string
	certFile                string
	keyFile                 string
	runtimeClassMode        string
	runtimeAdapterCutover   bool
	runtimeAdaptersConfig   string
	runtimeAdapterDiscovery bool
}

type configStore struct{ value atomic.Value }

func newConfigStore(cfg config.Config) *configStore {
	store := &configStore{}
	store.value.Store(cfg)
	return store
}

func (s *configStore) Current() config.Config  { return s.value.Load().(config.Config) }
func (s *configStore) Store(cfg config.Config) { s.value.Store(cfg) }

func run(opts options) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metricsSrv := &http.Server{
		Addr:              opts.metricsAddr,
		Handler:           httpapi.NewHealthHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("controller health server listening on %s", opts.metricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("controller health server error: %v", err)
			stop()
		}
	}()

	cfg, err := loadControllerConfig(opts)
	if err != nil {
		return err
	}
	cfg.RuntimeClassMode = config.RuntimeClassMode(opts.runtimeClassMode)
	switch cfg.RuntimeClassMode {
	case "", config.RuntimeClassModeInject:
		cfg.RuntimeClassMode = config.RuntimeClassModeInject
	case config.RuntimeClassModePreserve:
	default:
		return fmt.Errorf("unsupported runtime class mode %q", opts.runtimeClassMode)
	}

	store := newConfigStore(cfg)
	if opts.runtimeAdapterDiscovery {
		if err := startRuntimeCatalogReconciler(ctx, store, os.Getenv("POD_NAMESPACE")); err != nil {
			return err
		}
	}

	webhookSrv := &http.Server{
		Addr:              opts.addr,
		Handler:           httpapi.NewAdmissionHandlerWithConfigProvider(store.Current),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("controller webhook server listening on %s", opts.addr)
		if err := webhookSrv.ListenAndServeTLS(opts.certFile, opts.keyFile); err != nil && err != http.ErrServerClosed {
			log.Printf("controller webhook server error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(webhookSrv.Shutdown(shutdownCtx), metricsSrv.Shutdown(shutdownCtx))
}

func loadControllerConfig(opts options) (config.Config, error) {
	if opts.runtimeAdapterDiscovery {
		cfg := config.Default()
		cfg.RuntimeAdapterCutover = true
		return cfg, nil
	}
	return config.LoadAdmissionConfig(opts.runtimeAdaptersConfig, opts.runtimeAdapterCutover)
}

func startRuntimeCatalogReconciler(ctx context.Context, store *configStore, namespace string) error {
	if namespace == "" {
		namespace = "rootpv-system"
	}
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("runtime catalog discovery requires in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}
	reconciler := runtimecatalog.Reconciler{Client: client, Namespace: namespace}
	sync := func() {
		catalog, err := reconciler.Reconcile(ctx)
		if err != nil {
			log.Printf("reconcile runtime adapter catalog: %v", err)
			return
		}
		cfg := store.Current()
		cfg.RuntimeCatalog = catalog
		cfg.RuntimeAdapterCutover = true
		store.Store(cfg)
	}
	node.RunPeriodically(ctx, 2*time.Second, sync)
	return nil
}
