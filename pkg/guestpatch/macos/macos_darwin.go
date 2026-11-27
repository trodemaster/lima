// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package macos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/lima-vm/lima/v2/pkg/apfs"
	"github.com/lima-vm/lima/v2/pkg/imgutil/nativeimgutil/asifutil"
	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/osutil"
)

// attachImageWithRetry retries `diskutil image attach` because the
// command occasionally returns "Resource temporarily unavailable".
func attachImageWithRetry(ctx context.Context, disk string, retry int) (*asifutil.AttachedDisk, error) {
	var (
		attached *asifutil.AttachedDisk
		err      error
	)
	for range retry {
		attached, err = asifutil.DiskutilImageAttachNoMount(ctx, disk)
		if !errors.Is(err, asifutil.ErrResourceTemporarilyUnavailable) {
			break
		}
		time.Sleep(3 * time.Second)
	}
	return attached, err
}

// PatchOptions configures Patch.
type PatchOptions struct {
	// GuestProvisioning prepares the disk for a macOS 27 or later guest whose user
	// account Setup Assistant creates from the VZMacGuestProvisioningOptions passed
	// at start. It leaves out the markers that skip Setup Assistant, so that it runs
	// on the first boot, and grants the guest agent Full Disk Access, which macOS
	// requires to move the home directory Setup Assistant creates (both renaming it
	// and changing NFSHomeDirectory fail with EPERM without it).
	GuestProvisioning bool
}

// guestProvisioningTCCPermissions are added to the TCC permissions when
// PatchOptions.GuestProvisioning is set.
var guestProvisioningTCCPermissions = []limatype.TCCPermission{
	{Preset: "lima-guestagent-full-disk-access"},
}

// setupAssistantMarkers are the files that make macOS skip Setup Assistant,
// relative to the Data volume root.
var setupAssistantMarkers = []string{
	"private/var/db/.AppleSetupDone",
	"Library/User Template/.skipbuddy",
}

// Patch prepares a macOS guest disk for first boot. It writes the
// LaunchDaemon plist, init script, setup markers, and optional TCC
// permission entries via a noowners mount, then fixes file ownership
// by patching APFS inode records directly on the raw disk image.
// No sudo required.
func Patch(ctx context.Context, disk string, tccPerms []limatype.TCCPermission, opts PatchOptions) error {
	var markers []string
	if opts.GuestProvisioning {
		tccPerms = append(slices.Clone(tccPerms), guestProvisioningTCCPermissions...)
	} else {
		markers = setupAssistantMarkers
	}
	tccPaths, err := patchWriteGuestFiles(ctx, disk, markers, tccPerms)
	if err != nil {
		return err
	}
	return patchFixOwnership(ctx, disk, append(slices.Clone(markers), tccPaths...))
}

// patchWriteGuestFiles attaches the disk image, mounts the Data
// volume with noowners, writes guest files, then detaches.
// Returns the relative paths of any new filesystem entries created for TCC.
func patchWriteGuestFiles(ctx context.Context, disk string, markers []string, tccPerms []limatype.TCCPermission) ([]string, error) {
	attached, err := attachImageWithRetry(ctx, disk, 3)
	if err != nil {
		return nil, fmt.Errorf("failed to attach disk: %w", err)
	}
	if attached == nil || attached.Data == "" {
		return nil, errors.New("failed to find data slice in attached disk")
	}
	dataDevPath := "/dev/" + attached.Data
	defer func() {
		// Detaching the data slice is enough to detach the whole ASIF.
		if err := asifutil.DetachASIF(dataDevPath); err != nil {
			logrus.WithError(err).Warnf("failed to detach %#q (%#q)", dataDevPath, disk)
		}
	}()

	instDir := filepath.Dir(disk)
	mnt := filepath.Join(instDir, filenames.MntDir)
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create mount point %#q: %w", mnt, err)
	}
	defer func() {
		if err := os.Remove(mnt); err != nil {
			logrus.WithError(err).Warnf("failed to remove mount point %#q", mnt)
		}
	}()
	return writeGuestFiles(ctx, dataDevPath, mnt, markers, tccPerms)
}

// patchFixOwnership attaches the disk image and patches APFS inode
// records on the raw container device to set root:wheel ownership.
// extraPaths are paths relative to the Data volume root that were
// created during patchWriteGuestFiles and also need ownership fixed.
func patchFixOwnership(ctx context.Context, disk string, extraPaths []string) error {
	attached, err := attachImageWithRetry(ctx, disk, 3)
	if err != nil {
		return fmt.Errorf("failed to attach disk for ownership fix: %w", err)
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

	if attached.Container == "" {
		return errors.New("diskutil did not report an APFS container device")
	}

	// Patch APFS inode records via the raw container device.
	// The noowners mount stores files with uid=99 (nobody);
	// LaunchDaemon plists must be owned by root:wheel for launchd
	// to load them, so we patch them to UID 0 / GID 0 directly.
	containerDev := "/dev/r" + attached.Container
	paths := []string{
		"usr/local/sbin",
		"usr/local/sbin/lima-macos-init.sh",
		"Library/LaunchDaemons/io.lima-vm.lima-macos-init.plist",
	}
	paths = append(paths, extraPaths...)
	if err = apfs.Chown(containerDev, apfs.VolRoleData, 0, 0, paths...); err != nil {
		return fmt.Errorf("failed to fix file ownership on disk: %w", err)
	}

	return nil
}

// writeGuestFiles mounts the data volume with noowners and writes the
// LaunchDaemon plist, init script, the given setup markers, and optional TCC entries.
// Returns relative paths of any newly created TCC filesystem entries.
func writeGuestFiles(ctx context.Context, dataSliceDevice, mnt string, markers []string, tccPerms []limatype.TCCPermission) ([]string, error) {
	// Mount with "noowners" so non-root users can write to the volume.
	if err := osutil.Mount(ctx, "apfs", dataSliceDevice, mnt, []string{"noowners"}); err != nil {
		return nil, err
	}
	defer func() {
		if err := osutil.Umount(ctx, mnt); err != nil {
			logrus.WithError(err).Warnf("failed to unmount %#q", mnt)
		}
	}()

	for _, marker := range markers {
		file := filepath.Join(mnt, marker)
		if err := osutil.Touch(file); err != nil {
			return nil, fmt.Errorf("failed to touch %#q: %w", file, err)
		}
	}

	const initSh = `#!/bin/sh
set -eux
date
if [ ! -e /Volumes/cidata ]; then
  echo "/Volumes/cidata is not mounted" >&2
  exit 1
fi
exec /Volumes/cidata/lima-guestagent fake-cloud-init
`
	if err := os.MkdirAll(filepath.Join(mnt, "usr/local/sbin"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(mnt, "usr/local/sbin/lima-macos-init.sh"), []byte(initSh), 0o755); err != nil {
		return nil, err
	}

	const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>io.lima-vm.lima-macos-init</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/sbin/lima-macos-init.sh</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/dev/tty.virtio</string>
    <key>StandardErrorPath</key>
    <string>/dev/tty.virtio</string>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(mnt, "Library/LaunchDaemons/io.lima-vm.lima-macos-init.plist"), []byte(plist), 0o755); err != nil {
		return nil, err
	}

	tccPaths, err := patchTCC(ctx, mnt, tccPerms)
	if err != nil {
		return nil, fmt.Errorf("failed to patch TCC database: %w", err)
	}
	return tccPaths, nil
}
