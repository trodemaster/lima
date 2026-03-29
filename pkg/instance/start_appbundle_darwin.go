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
	dirs, err := usrlocal.ShareLima()
	if err != nil {
		return ""
	}
	for _, dir := range dirs {
		bundle := filepath.Join(dir, appBundleName)
		info, err := os.Stat(filepath.Join(bundle, "Contents", "Info.plist"))
		if err == nil && !info.IsDir() {
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
