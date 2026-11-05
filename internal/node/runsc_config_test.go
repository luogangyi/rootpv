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

package node

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

type runscFixture struct {
	root, path string
	source     map[string]any
	original   []byte
}

func runscSourceFixture(t *testing.T) runscFixture {
	t.Helper()
	root := t.TempDir()
	writeExecutable(t, root, "opt/gvisor/runsc")
	writeExecutable(t, root, "usr/local/bin/rootfs-runtime-wrapper")
	path := filepath.Join(root, "etc/containerd/runsc.toml")
	original := []byte(`binary_name = "/opt/gvisor/runsc"
grouping = true
log_level = "debug"
[runsc_config]
platform = "systrap"
overlay2 = "root:self"
`)
	writeRuntimeConfigFile(t, path, original)
	writeRuntimeConfigFile(t, filepath.Join(root, "etc/containerd/runsc/config.toml"), []byte("grouping = true\n"))
	var source map[string]any
	if _, err := toml.Decode(`runtime_type = "io.containerd.runsc.v1"
runtime_path = "/opt/gvisor/containerd-shim-runsc-v1"
pod_annotations = ["dev.gvisor.*"]
container_annotations = ["dev.gvisor.*"]
[options]
TypeUrl = "io.containerd.runsc.v1.options"
ConfigPath = "/etc/containerd/runsc.toml"
`, &source); err != nil {
		t.Fatal(err)
	}
	return runscFixture{root: root, path: path, source: source, original: original}
}

func TestRunscDiscoveryAndShadowConfig(t *testing.T) {
	fixture := runscSourceFixture(t)
	root, path, source, original := fixture.root, fixture.path, fixture.source, fixture.original
	handler := discoverHandler("runsc", source, (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults())
	if !handler.Resolved || handler.AdapterID != "runsc-v1" {
		t.Fatalf("handler: %#v", handler)
	}
	raw := map[string]any{"plugins": map[string]any{"io.containerd.cri.v1.runtime": map[string]any{"containerd": map[string]any{"runtimes": map[string]any{"runsc": source}}}}}
	adapter := config.CatalogRuntimeAdapter{ID: handler.AdapterID, SourceHandler: "runsc", RuntimeType: config.RunscRuntimeType, RootfsHandler: "persistent-rootfs-runsc-v1", ClusterDigest: "cluster"}
	opts := RuntimeReconcileOptions{HostRoot: root, WrapperBinary: "/usr/local/bin/rootfs-runtime-wrapper", RegistryPath: filepath.Join(root, "etc/rootpv/registry.yaml")}
	entry, ready, err := reconcileCatalogAdapter(raw, raw, adapter, opts)
	if err != nil || !ready || entry.DelegateBinary != "/opt/gvisor/runsc" {
		t.Fatalf("entry=%#v, ready=%v, error=%v", entry, ready, err)
	}
	assertRunscShadow(t, root, source, adapterSource(raw, adapter.RootfsHandler))
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("source file changed: %v", err)
	}
	if stringValue(mapValue(source["options"])["ConfigPath"]) != "/etc/containerd/runsc.toml" {
		t.Fatal("source handler mutated")
	}
}

func assertRunscShadow(t *testing.T, root string, source, shadow map[string]any) {
	t.Helper()
	options := mapValue(shadow["options"])
	if _, ok := options["BinaryName"]; ok {
		t.Fatal("runsc received runc BinaryName")
	}
	if shadow["runtime_path"] != source["runtime_path"] {
		t.Fatal("vendor shim path changed")
	}
	for _, key := range []string{"pod_annotations", "container_annotations"} {
		annotations, ok := shadow[key]
		if !ok || !reflect.DeepEqual(annotations, []string{"dev.gvisor.*", "openfuyao.cn/*"}) {
			t.Fatalf("%s allowlist: %#v", key, annotations)
		}
	}
	generated := hostPath(root, stringValue(options["ConfigPath"]))
	var raw map[string]any
	if _, err := toml.DecodeFile(generated, &raw); err != nil {
		t.Fatal(err)
	}
	binary, hasBinary := raw["binary_name"]
	grouping, hasGrouping := raw["grouping"]
	if !hasBinary || !hasGrouping || binary != "/usr/local/bin/rootfs-runtime-wrapper-runsc-v1" || grouping != true {
		t.Fatalf("generated config: %#v", raw)
	}
	flags, hasFlags := raw["runsc_config"]
	if !hasFlags || !reflect.DeepEqual(mapValue(flags), map[string]any{"platform": "systrap", "overlay2": "none"}) {
		t.Fatal("shim flags not preserved")
	}
	info, err := os.Stat(generated)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatal("generated config permissions")
	}
}

func TestRunscDiscoveryFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"missing binary", "grouping = true", "runsc_binary_path_required"},
		{"relative binary", "binary_name = 'runsc'", "runsc_binary_path_required"},
		{"absent binary", "binary_name = '/missing/runsc'", "delegate_missing"},
		{"invalid flag", "binary_name = '/opt/gvisor/runsc'\n[runsc_config]\ndebug = true\n", "runsc_flags_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := runscSourceFixture(t)
			root, path, source := fixture.root, fixture.path, fixture.source
			writeRuntimeConfigFile(t, path, []byte(tc.body))
			handler := discoverHandler("runsc", source, (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults())
			if handler.Resolved || handler.Reason != tc.want {
				t.Fatalf("handler: %#v", handler)
			}
		})
	}
}

func TestRunscDigestChangesWithShimOptions(t *testing.T) {
	fixture := runscSourceFixture(t)
	root, path, source := fixture.root, fixture.path, fixture.source
	opts := (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults()
	before := discoverHandler("runsc", source, opts)
	writeRuntimeConfigFile(t, path, []byte("binary_name = '/opt/gvisor/runsc'\n[runsc_config]\nplatform = 'kvm'\n"))
	after := discoverHandler("runsc", source, opts)
	if !before.Resolved || !after.Resolved || before.NodeResolvedDigest == after.NodeResolvedDigest {
		t.Fatalf("digest not updated: %#v %#v", before, after)
	}
}

func TestRunscDiscoveryRequiresExplicitShimOptions(t *testing.T) {
	for _, tc := range []struct{ key, value, want string }{
		{"TypeUrl", "", "runsc_options_type_required"},
		{"ConfigPath", "runsc.toml", "runsc_config_path_required"},
		{"ConfigPath", "/missing.toml", "runsc_config_unreadable"},
	} {
		fixture := runscSourceFixture(t)
		root, source := fixture.root, fixture.source
		mapValue(source["options"])[tc.key] = tc.value
		handler := discoverHandler("runsc", source, (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults())
		if handler.Resolved || handler.Reason != tc.want {
			t.Fatalf("handler: %#v", handler)
		}
	}
}

func TestRunscDiscoveryRequiresBootstrapGrouping(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"missing", "", "runsc_shim_grouping_required"},
		{"disabled", "grouping = false\n", "runsc_shim_grouping_required"},
		{"unset", "log_level = 'info'\n", "runsc_shim_grouping_required"},
		{"invalid", "grouping = 'true'\n", "runsc_shim_config_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := runscSourceFixture(t)
			root, source := fixture.root, fixture.source
			path := filepath.Join(root, "etc/containerd/runsc/config.toml")
			if tc.body == "" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				writeRuntimeConfigFile(t, path, []byte(tc.body))
			}
			handler := discoverHandler("runsc", source, (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults())
			if handler.Resolved || handler.Reason != tc.want {
				t.Fatalf("handler: %#v", handler)
			}
		})
	}
}

func TestRunscBootstrapPrecedenceAndDigest(t *testing.T) {
	fixture := runscSourceFixture(t)
	root, source := fixture.root, fixture.source
	opts := (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults()
	before := discoverHandler("runsc", source, opts)
	path := filepath.Join(root, "run/containerd/runsc/config.toml")
	writeRuntimeConfigFile(t, path, []byte("grouping = false\n"))
	blocked := discoverHandler("runsc", source, opts)
	if blocked.Resolved || blocked.Reason != "runsc_shim_grouping_required" {
		t.Fatalf("higher-priority config ignored: %#v", blocked)
	}
	writeRuntimeConfigFile(t, path, []byte("grouping = true\n"))
	after := discoverHandler("runsc", source, opts)
	if !before.Resolved || !after.Resolved || before.NodeResolvedDigest == after.NodeResolvedDigest {
		t.Fatalf("bootstrap path missing from digest: %#v %#v", before, after)
	}
	writeRuntimeConfigFile(t, path, []byte("grouping = true\nlog_level = 'debug'\n"))
	changed := discoverHandler("runsc", source, opts)
	if !changed.Resolved || changed.NodeResolvedDigest == after.NodeResolvedDigest {
		t.Fatalf("bootstrap content missing from digest: %#v", changed)
	}
}

func TestRunscReconcileRejectsMissingBootstrapGrouping(t *testing.T) {
	fixture := runscSourceFixture(t)
	root, source := fixture.root, fixture.source
	if err := os.Remove(filepath.Join(root, "etc/containerd/runsc/config.toml")); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{"plugins": map[string]any{"io.containerd.cri.v1.runtime": map[string]any{"containerd": map[string]any{"runtimes": map[string]any{"runsc": source}}}}}
	adapter := config.CatalogRuntimeAdapter{ID: "runsc-v1", SourceHandler: "runsc", RuntimeType: config.RunscRuntimeType, RootfsHandler: "persistent-rootfs-runsc-v1", ClusterDigest: "cluster"}
	opts := RuntimeReconcileOptions{HostRoot: root, WrapperBinary: "/usr/local/bin/rootfs-runtime-wrapper", RegistryPath: filepath.Join(root, "etc/rootpv/registry.yaml")}
	_, ready, err := reconcileCatalogAdapter(raw, raw, adapter, opts)
	if err == nil || ready || len(adapterSource(raw, adapter.RootfsHandler)) != 0 {
		t.Fatalf("unsafe handler became ready: ready=%v error=%v", ready, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "usr/local/bin/rootfs-runtime-wrapper-runsc-v1")); !os.IsNotExist(err) {
		t.Fatalf("unexpected wrapper alias: %v", err)
	}
}

func TestRunscImportedConfigAndFailureIsolation(t *testing.T) {
	f := runscSourceFixture(t)
	writeExecutable(t, f.root, "usr/bin/runc")
	raw := map[string]any{"plugins": map[string]any{"io.containerd.cri.v1.runtime": map[string]any{"containerd": map[string]any{"runtimes": map[string]any{"runsc": f.source}}}}}
	vendor := filepath.Join(f.root, "etc/containerd/vendor.toml")
	writeRuntimeConfigFile(t, vendor, nil)
	if _, err := writeTOMLIfChanged(vendor, nil, raw); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, "etc/containerd/config.toml")
	writeRuntimeConfigFile(t, path, []byte("imports = ['vendor.toml']\n"))
	catalog := config.RuntimeCatalog{FormatVersion: config.RuntimeCatalogFormatVersion, Adapters: []config.CatalogRuntimeAdapter{
		{ID: "runsc-v1", SourceHandler: "runsc", RuntimeType: config.RunscRuntimeType, RootfsHandler: "persistent-rootfs-runsc-v1", ClusterDigest: "runsc"},
		{ID: "runc-v2", SourceHandler: "runc", RuntimeType: config.RuncV2RuntimeType, RootfsHandler: "persistent-rootfs-runc-v2", ClusterDigest: "runc"},
	}}
	opts := RuntimeReconcileOptions{HostRoot: f.root, WrapperBinary: "/usr/local/bin/rootfs-runtime-wrapper", RegistryPath: filepath.Join(f.root, "etc/rootpv/registry.yaml")}
	result, err := ReconcileRuntimeAdapters(path, catalog, opts)
	if err != nil || len(result.Readiness) != 2 {
		t.Fatalf("imported adapters unavailable: %#v %v", result, err)
	}
	if result, err = ReconcileRuntimeAdapters(path, catalog, opts); err != nil || result.Changed {
		t.Fatalf("not idempotent: %#v %v", result, err)
	}
	shadow := mapValue(decodeRuntimeMap(t, path)["persistent-rootfs-runsc-v1"])
	generated := hostPath(f.root, stringValue(mapValue(shadow["options"])["ConfigPath"]))
	if err := os.Remove(generated); err != nil {
		t.Fatal(err)
	}
	if result, err = ReconcileRuntimeAdapters(path, catalog, opts); err != nil || !result.Changed {
		t.Fatalf("missing shim config not reactivated: %#v %v", result, err)
	}
	writeRuntimeConfigFile(t, f.path, []byte("binary_name = 'relative'"))
	result, err = ReconcileRuntimeAdapters(path, catalog, opts)
	if err != nil || result.Readiness["runc-v2"] == "" || result.Unavailable["runsc-v1"] == "" {
		t.Fatalf("runsc blocked healthy adapter: %#v %v", result, err)
	}
}

func TestAdapterReloadFailureAndRetry(t *testing.T) {
	f := prepareVendorRuntimeReconcileFixture(t)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{"openfuyao.cn/rootpv-adapter-vendor-gpu-runc-v2": "old"}}}
	client := fake.NewSimpleClientset(node)
	opts := RuntimeAdapterReconcilerOptions{ContainerdConfig: f.configPath, HostRoot: f.root, WrapperBinary: f.options.WrapperBinary, RegistryPath: f.registryPath}
	marker := f.registryPath + ".pending-reload"
	for _, command := range []string{"exit 1", "", "true"} {
		opts.RestartCommand = command
		result, err := reconcileAndActivateAdapters(context.Background(), client, "worker", &opts, f.catalog)
		if command == "true" {
			if err != nil || len(result.Readiness) != 1 {
				t.Fatalf("retry failed: %#v %v", result, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("pending reload not cleared: %v", err)
			}
		} else {
			if err == nil {
				t.Fatal("failed reload accepted")
			}
			current, getErr := client.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
			if getErr != nil {
				t.Fatal(getErr)
			}
			if current.Labels["openfuyao.cn/rootpv-adapter-vendor-gpu-runc-v2"] != "" {
				t.Fatal("failed activation remained ready")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := reconcileAndActivateAdapters(context.Background(), client, "worker", &opts, f.catalog)
	if err != nil || result.Changed {
		t.Fatalf("activated config not idempotent: %#v %v", result, err)
	}
}

func TestAdapterReloadDoesNotRepeatAfterMarkerCleanupFailure(t *testing.T) {
	f := prepareVendorRuntimeReconcileFixture(t)
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}})
	marker, reloadLog := f.registryPath+".pending-reload", filepath.Join(f.root, "reloads")
	t.Setenv("ROOTPV_TEST_MARKER", marker)
	t.Setenv("ROOTPV_TEST_RELOAD_LOG", reloadLog)
	opts := RuntimeAdapterReconcilerOptions{
		ContainerdConfig: f.configPath, HostRoot: f.root, WrapperBinary: f.options.WrapperBinary, RegistryPath: f.registryPath,
		RestartCommand: `printf x >> "$ROOTPV_TEST_RELOAD_LOG"; rm "$ROOTPV_TEST_MARKER"; mkdir "$ROOTPV_TEST_MARKER"; touch "$ROOTPV_TEST_MARKER/keep"`,
	}
	for i := 0; i < 2; i++ {
		result, err := reconcileAndActivateAdapters(context.Background(), client, "worker", &opts, f.catalog)
		if err != nil || len(result.Readiness) != 1 {
			t.Fatalf("successful activation lost after cleanup failure: %#v %v", result, err)
		}
	}
	if data, err := os.ReadFile(reloadLog); err != nil || string(data) != "x" {
		t.Fatalf("unchanged config reloaded: %q %v", data, err)
	}
	if err := os.RemoveAll(marker); err != nil {
		t.Fatal(err)
	}
	f.catalog.Generation++
	if _, err := reconcileAndActivateAdapters(context.Background(), client, "worker", &opts, f.catalog); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(reloadLog); err != nil || string(data) != "xx" {
		t.Fatalf("new config did not reload: %q %v", data, err)
	}
}

func TestCatalogReadFailureWithdrawsAdapterReadiness(t *testing.T) {
	const label = "openfuyao.cn/rootpv-adapter-runsc-v1"
	for _, skip := range []bool{false, true} {
		client := fake.NewSimpleClientset(
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{label: "old", "unrelated": "kept"}}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: config.RuntimeCatalogConfigMapName, Namespace: "test"}, Data: map[string]string{config.RuntimeCatalogDataKey: "["}},
		)
		opts := RuntimeAdapterReconcilerOptions{Namespace: "test", SkipReadiness: skip}
		syncRuntimeAdapters(context.Background(), client, "worker", &opts, "")
		node, err := client.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"unrelated": "kept"}
		if skip {
			want[label] = "old"
		}
		if !reflect.DeepEqual(node.Labels, want) {
			t.Fatalf("skipReadiness=%v: labels=%v, want %v", skip, node.Labels, want)
		}
	}
}
