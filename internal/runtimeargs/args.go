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
	"fmt"
	"path/filepath"
	"strings"
)

// Global returns a copy of flags preceding a create, run, or delete command.
func Global(args []string) []string {
	for i, arg := range args {
		if arg == "create" || arg == "run" || arg == "delete" {
			return append([]string(nil), args[:i]...)
		}
	}
	return nil
}

// RunscRoot returns the normalized absolute root, or an empty string if absent.
// A present root flag must have an absolute path, including on repeated flags.
func RunscRoot(global []string) (string, error) {
	root := ""
	needsRoot := false
	for _, arg := range global {
		switch {
		case needsRoot:
			root, needsRoot = arg, false
		case strings.HasPrefix(arg, "--root=") || strings.HasPrefix(arg, "-root="):
			_, root, _ = strings.Cut(arg, "=")
		case arg == "--root" || arg == "-root":
			needsRoot = true
			continue
		default:
			continue
		}
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("runsc --root requires an absolute path, got %q", root)
		}
	}
	if needsRoot {
		return "", fmt.Errorf("runsc --root requires a value")
	}
	if root != "" {
		root = filepath.Clean(root)
	}
	return root, nil
}

// RunscSavedGlobal prefers pinned delete flags while independently validating
// their root against the original invocation, returning the validated root too.
// Older states may lack either invocation.
func RunscSavedGlobal(createArgs, deleteArgs []string) ([]string, string, error) {
	createGlobal := Global(createArgs)
	createRoot, err := RunscRoot(createGlobal)
	if err != nil {
		return nil, "", err
	}
	if len(deleteArgs) == 0 {
		return createGlobal, createRoot, nil
	}
	deleteGlobal := Global(deleteArgs)
	deleteRoot, err := RunscRoot(deleteGlobal)
	if err != nil {
		return nil, "", err
	}
	if deleteRoot == "" {
		return nil, "", fmt.Errorf("saved runsc delete requires an explicit --root")
	}
	if createRoot != "" && createRoot != deleteRoot {
		return nil, "", fmt.Errorf("saved runsc delete root differs from saved runtime root")
	}
	return deleteGlobal, deleteRoot, nil
}
