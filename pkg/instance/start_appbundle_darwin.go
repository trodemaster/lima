//go:build darwin

// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package instance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sirupsen/logrus"

	"github.com/lima-vm/lima/v2/pkg/usrlocal"
)

const appBundleName = "Lima.app"

func findAppBundle() string {
	// Search for Lima.app in order of precedence:
	// 1. Standard macOS Applications directory (highest priority)
	// 2. ShareLima() paths derived from the running binary's prefix
	// 3. Explicit prefix share/lima fallbacks
	var searchPaths []string

	// Standard macOS Applications location
	searchPaths = append(searchPaths,
		"/Applications",
	)

	// Paths relative to the running binary (covers dev builds, Homebrew, etc.)
	if dirs, err := usrlocal.ShareLima(); err == nil {
		searchPaths = append(searchPaths, dirs...)
	}

	// Explicit prefix share/lima paths as a final fallback
	searchPaths = append(searchPaths,
		"/opt/local/share/lima",
		"/usr/local/share/lima",
	)

	for _, dir := range searchPaths {
		bundle := filepath.Join(dir, appBundleName)
		info, err := os.Stat(filepath.Join(bundle, "Contents", "Info.plist"))
		if err == nil && !info.IsDir() {
			logrus.Debugf("Found app bundle at: %s", bundle)
			return bundle
		}
	}
	return ""
}

func launchHostAgentInAppBundle(bundlePath string, args []string, stdoutPath, stderrPath string) (*exec.Cmd, error) {
	openArgs := []string{"-n", "-a", bundlePath,
		"--stdout", stdoutPath,
		"--stderr", stderrPath,
		"--args"}
	openArgs = append(openArgs, args...)

	logrus.Infof("Launching hostagent via app bundle: open %v", openArgs)

	cmd := exec.Command("/usr/bin/open", openArgs...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to launch hostagent via app bundle %q: %w", bundlePath, err)
	}
	return cmd, nil
}
