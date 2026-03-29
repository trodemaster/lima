//go:build !darwin

// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package instance

import (
	"fmt"
	"os/exec"
)

func findAppBundle() string {
	return ""
}

func launchHostAgentInAppBundle(_ string, _ []string, _, _ string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("app bundle launch is only supported on macOS")
}
