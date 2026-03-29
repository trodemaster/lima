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
	// Search in multiple locations:
	// 1. Standard Lima share directories (dev/installed via brew, etc)
	// 2. MacPorts default prefix: /usr/local
	// 3. MacPorts alternate prefix: /opt/local (common on macOS with Xcode)
	searchPaths := []string{}
	
	// Add ShareLima() paths (includes debug workspace and system prefixes)
	if dirs, err := usrlocal.ShareLima(); err == nil {
		searchPaths = append(searchPaths, dirs...)
	}
	
	// Add standard macports locations
	searchPaths = append(searchPaths, []string{
		"/usr/local/share/lima",
		"/opt/local/share/lima",
	}...)
	
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
