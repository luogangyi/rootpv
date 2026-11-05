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

package runtimeargs

import (
	"reflect"
	"testing"
)

func TestRunscRoot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		root    string
		invalid bool
	}{
		{name: "absent"},
		{name: "equals and normalization", args: []string{"--root=/run/other/../runsc"}, root: "/run/runsc"},
		{name: "separate single dash", args: []string{"-root", "/run/runsc"}, root: "/run/runsc"},
		{name: "missing value", args: []string{"--root"}, invalid: true},
		{name: "empty value", args: []string{"--root="}, invalid: true},
		{name: "relative value", args: []string{"--root=runsc"}, invalid: true},
		{name: "earlier relative value", args: []string{"--root=relative", "--root=/run/runsc"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := RunscRoot(tc.args)
			if (err != nil) != tc.invalid || root != tc.root {
				t.Fatalf("root=%q error=%v", root, err)
			}
		})
	}
}

func TestRunscSavedGlobal(t *testing.T) {
	create := []string{"--root=/run/runsc", "create", "container"}
	remove := []string{"--root", "/run/runsc", "--debug=true", "delete", "container"}
	for _, tc := range []struct {
		name                 string
		create, remove, want []string
		root                 string
		invalid              bool
	}{
		{name: "legacy create", create: create, want: create[:1], root: "/run/runsc"},
		{name: "pinned delete", create: create, remove: remove, want: remove[:3], root: "/run/runsc"},
		{name: "delete only", remove: remove, want: remove[:3], root: "/run/runsc"},
		{name: "conflicting roots", create: create, remove: []string{"--root=/other", "delete", "container"}, invalid: true},
		{name: "missing delete root", create: create, remove: []string{"delete", "container"}, invalid: true},
		{name: "invalid create root", create: []string{"--root=relative", "create", "container"}, remove: remove, invalid: true},
		{name: "invalid delete root", create: create, remove: []string{"--root", "delete", "container"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global, root, err := RunscSavedGlobal(tc.create, tc.remove)
			if (err != nil) != tc.invalid || root != tc.root || !reflect.DeepEqual(global, tc.want) {
				t.Fatalf("global=%v root=%q error=%v", global, root, err)
			}
		})
	}
}
