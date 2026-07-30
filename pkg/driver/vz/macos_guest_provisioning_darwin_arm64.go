// macos_guest_provisioning_darwin_arm64.go
// Go wrapper for VZMacGuestProvisioningOptions provisioning boot (macOS 27+).
//
// RunGuestProvisioningBoot() runs a transient "provisioning boot" after the OS
// install completes. macOS applies the user account settings during this first
// boot, and the VM is then stopped so Lima's normal Start() becomes the clean
// second boot with the account already configured.
//
// This requires macOS 27+ on both host and guest. On macOS 26.x the function
// returns a non-fatal error and Lima falls back to configure.sh for setup.

//go:build darwin && arm64 && !no_vz

package vz

/*
#cgo CFLAGS: -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Foundation -framework Virtualization

#import "macos_guest_provisioning_darwin_arm64.h"

static void freeProvErrString(char *s) { free(s); }
*/
import "C"
import (
	"context"
	"fmt"
	"path/filepath"
	"unsafe"

	"github.com/docker/go-units"
	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/sirupsen/logrus"
)

// GuestProvisioningAvailable reports whether VZMacGuestProvisioningOptions is
// available on the current host OS (requires macOS 27+).
func GuestProvisioningAvailable() bool {
	return C.guestProvisioningAvailable() != 0
}

// RunGuestProvisioningBoot runs a transient provisioning first-boot for the
// macOS guest. macOS applies the VZMacGuestProvisioningOptions (user account,
// autologin, SSH) during this boot, then the VM is stopped.
//
// Returns a non-nil error on macOS < 27 or on provisioning failure. Callers
// should treat a non-nil error as non-fatal and log a warning; Lima's existing
// configure.sh will handle account setup via SSH as usual.
func RunGuestProvisioningBoot(_ context.Context, inst *limatype.Instance, gp *limatype.VZGuestProvisioning) error {
	for _, f := range []string{filenames.VzIdentifier, filenames.VzHwModel, filenames.Disk} {
		if err := checkFileExists(filepath.Join(inst.Dir, f)); err != nil {
			return fmt.Errorf("guest provisioning prerequisite missing: %w", err)
		}
	}

	memBytes, err := units.RAMInBytes(*inst.Config.Memory)
	if err != nil {
		return fmt.Errorf("invalid memory size: %w", err)
	}

	cDir := C.CString(inst.Dir)
	defer C.free(unsafe.Pointer(cDir))
	cFullName := C.CString(gp.FullName)
	defer C.free(unsafe.Pointer(cFullName))
	cUsername := C.CString(gp.Username)
	defer C.free(unsafe.Pointer(cUsername))
	cPassword := C.CString(gp.Password)
	defer C.free(unsafe.Pointer(cPassword))

	logrus.Infof("Guest provisioning: starting first-boot (user=%q, autologin=%v, remoteLogin=%v)",
		gp.Username, gp.LogsInAutomatically, gp.EnablesRemoteLogin)

	cErr := C.runGuestProvisioningBoot(
		cDir,
		C.uint(uint(*inst.Config.CPUs)),
		C.ulonglong(uint64(memBytes)),
		cFullName,
		cUsername,
		cPassword,
		C.bool(gp.LogsInAutomatically),
		C.bool(gp.EnablesRemoteLogin),
		60,  // bootTimeoutSecs: wait up to 60s for VM to reach Running
		300, // provisionTimeoutSecs: wait up to 5 min for macOS first-boot setup
		60,  // stopTimeoutSecs: wait up to 60s for graceful stop after requestStop
	)
	if cErr != nil {
		defer C.freeProvErrString(cErr)
		return fmt.Errorf("guest provisioning boot: %s", C.GoString(cErr))
	}

	logrus.Info("Guest provisioning: first-boot complete")
	return nil
}
