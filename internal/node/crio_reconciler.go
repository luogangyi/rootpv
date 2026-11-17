// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package node

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/crio"
)

func StartCRIOInventoryPublisher(ctx context.Context, nodeName, socket, hostRoot string) {
	client, err := inClusterClient()
	if err != nil {
		log.Printf("CRI-O inventory publisher: %v", err)
		return
	}
	RunPeriodically(ctx, 2*time.Second, func() {
		node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			log.Printf("get node for CRI-O inventory: %v", err)
			return
		}
		inventory, err := DiscoverCRIOInventory(ctx, socket, RuntimeDiscoveryOptions{NodeUID: string(node.UID), HostRoot: hostRoot})
		if err == nil {
			err = PublishRuntimeInventory(ctx, client, nodeName, inventory)
		}
		if err != nil {
			log.Printf("publish CRI-O inventory: %v", err)
		}
	})
}

func reconcileAndActivateCRIO(ctx context.Context, client kubernetes.Interface, nodeName string, opts *RuntimeAdapterReconcilerOptions, catalog config.RuntimeCatalog) (RuntimeReconcileResult, error) {
	activation, err := loadCRIOActivation(*opts)
	if err != nil {
		return RuntimeReconcileResult{}, err
	}
	withdraw := func() error {
		return opts.publishReadiness(ctx, client, nodeName, nil)
	}
	beforeWrite := func() error {
		if err := withdraw(); err != nil {
			return err
		}
		return activation.mark(ctx, *opts)
	}
	result, err := ReconcileCRIOAdapters(ctx, opts.CRIOSocket, opts.CRIODropIn, catalog, RuntimeReconcileOptions{HostRoot: opts.HostRoot, WrapperBinary: opts.WrapperBinary, RegistryPath: opts.RegistryPath, beforeWrite: beforeWrite, beforeRegistryWrite: withdraw})
	if err != nil {
		return result, err
	}
	if err := activation.ensurePending(beforeWrite); err != nil {
		return result, err
	}
	if activation.pending {
		if err := opts.publishReadiness(ctx, client, nodeName, nil); err != nil {
			return result, err
		}
		if err := activation.restart(ctx, *opts); err != nil {
			return result, err
		}
	}
	if err := verifyCRIOHandlers(ctx, *opts); err != nil {
		return result, err
	}
	return result, activation.complete()
}

type crioActivation struct {
	path, before string
	pending      bool
}

func (a *crioActivation) ensurePending(mark func() error) error {
	if a.pending {
		return nil
	}
	_, err := os.Stat(a.path + ".active")
	if os.IsNotExist(err) {
		return mark()
	}
	return err
}

// complete persists verified activation before removing the restart intent.
func (a *crioActivation) complete() error {
	if !a.pending {
		return nil
	}
	if err := writeFileAtomicMode(a.path+".active", []byte("restart verified\n"), 0o600); err != nil {
		return err
	}
	err := os.Remove(a.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func loadCRIOActivation(opts RuntimeAdapterReconcilerOptions) (*crioActivation, error) {
	activation := &crioActivation{path: opts.RegistryPath + ".pending-reload"}
	data, err := os.ReadFile(activation.path)
	if os.IsNotExist(err) {
		return activation, nil
	}
	if err != nil {
		return nil, err
	}
	activation.before = string(data)
	if err := validateCRIOActivationIdentity(activation.before); err != nil {
		return nil, err
	}
	activation.pending = true
	return activation, nil
}

func validateCRIOActivationIdentity(identity string) error {
	fields := strings.Split(identity, ":")
	if len(fields) != 3 || fields[0] == "" {
		return fmt.Errorf("invalid CRI-O activation identity")
	}
	for _, value := range fields[1:] {
		if number, err := strconv.ParseUint(value, 10, 64); err != nil || number == 0 {
			return fmt.Errorf("invalid CRI-O activation process identity")
		}
	}
	return nil
}

func (a *crioActivation) mark(ctx context.Context, opts RuntimeAdapterReconcilerOptions) error {
	identity, err := (crio.Client{Socket: opts.CRIOSocket}).PeerIdentity(ctx)
	if err != nil {
		return err
	}
	if err := writeFileAtomicMode(a.path, []byte(identity), 0o600); err != nil {
		return err
	}
	a.before, a.pending = identity, true
	return nil
}

func (a *crioActivation) restart(ctx context.Context, opts RuntimeAdapterReconcilerOptions) error {
	client := crio.Client{Socket: opts.CRIOSocket}
	identity, err := client.PeerIdentity(ctx)
	if err != nil {
		return err
	}
	if identity == a.before {
		if err := restartCRIODaemon(ctx, opts.RestartCommand); err != nil {
			return err
		}
	}
	return a.waitForRestart(ctx, client)
}

func restartCRIODaemon(ctx context.Context, command string) error {
	if command == "" {
		return fmt.Errorf("CRI-O restart command required")
	}
	restartCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return exec.CommandContext(restartCtx, "/bin/sh", "-ec", command).Run()
}

func (a *crioActivation) waitForRestart(ctx context.Context, client crio.Client) error {
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		identity, err := client.PeerIdentity(deadline)
		if err == nil && identity != a.before {
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("CRI-O activation requires a daemon restart; SIGHUP alone is insufficient: %w", deadline.Err())
		case <-ticker.C:
		}
	}
}
