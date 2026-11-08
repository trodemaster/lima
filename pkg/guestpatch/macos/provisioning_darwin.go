// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package macos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"

	"github.com/lima-vm/lima/v2/pkg/imgutil/nativeimgutil/asifutil"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/osutil"
)

// setupAssistantMarkers are the files Patch writes to skip Setup Assistant.
var setupAssistantMarkers = []string{
	"private/var/db/.AppleSetupDone",
	"Library/User Template/.skipbuddy",
}

// RemoveSetupAssistantMarkers removes the Setup Assistant skip markers written
// by Patch, so that the first boot runs Setup Assistant, which is expected to
// apply VZMacGuestProvisioningOptions (macOS 27 or later).
func RemoveSetupAssistantMarkers(ctx context.Context, disk string) error {
	attached, err := attachImageWithRetry(ctx, disk, 3)
	if err != nil {
		return fmt.Errorf("failed to attach disk: %w", err)
	}
	if attached == nil || attached.Data == "" {
		return errors.New("failed to find data slice in attached disk")
	}
	dataDevPath := "/dev/" + attached.Data
	defer func() {
		if err := asifutil.DetachASIF(dataDevPath); err != nil {
			logrus.WithError(err).Warnf("failed to detach %#q (%#q)", dataDevPath, disk)
		}
	}()

	mnt := filepath.Join(filepath.Dir(disk), filenames.MntDir)
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return fmt.Errorf("failed to create mount point %#q: %w", mnt, err)
	}
	defer func() {
		if err := os.Remove(mnt); err != nil {
			logrus.WithError(err).Warnf("failed to remove mount point %#q", mnt)
		}
	}()
	if err := osutil.Mount(ctx, "apfs", dataDevPath, mnt, []string{"noowners"}); err != nil {
		return err
	}
	defer func() {
		if err := osutil.Umount(ctx, mnt); err != nil {
			logrus.WithError(err).Warnf("failed to unmount %#q", mnt)
		}
	}()

	for _, f := range setupAssistantMarkers {
		if err := os.Remove(filepath.Join(mnt, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to remove %#q: %w", f, err)
		}
	}
	return nil
}
