// macos_guest_provisioning_darwin_arm64.h
// CGo bridge for VZMacGuestProvisioningOptions (macOS 27+).
//
// This uses runtime ObjC message passing so it compiles against any SDK;
// guestProvisioningAvailable() returns 0 at runtime on pre-27 hosts.

#pragma once
#include <stdlib.h>
#include <stdbool.h>

// Returns 1 if VZMacGuestProvisioningOptions is available at runtime (macOS 27+).
int guestProvisioningAvailable(void);

// Runs a provisioning first-boot cycle:
//   1. Builds a minimal VZVirtualMachine from instDir (vz-identifier, vz-hwmodel, vz-aux, disk).
//   2. Starts it with VZMacGuestProvisioningOptions so macOS creates the user account.
//   3. Waits up to bootTimeoutSecs for the VM to reach Running state.
//   4. Waits up to provisionTimeoutSecs for macOS first-boot setup; then requests stop.
//   5. Waits up to stopTimeoutSecs for the VM to reach Stopped state.
//
// Returns NULL on success, or a malloc'd error C-string on failure (caller must free).
char *runGuestProvisioningBoot(
    const char *instDir,
    unsigned int cpuCount,
    unsigned long long memorySizeBytes,
    const char *fullName,
    const char *username,
    const char *password,
    bool logsInAutomatically,
    bool enablesRemoteLogin,
    int bootTimeoutSecs,
    int provisionTimeoutSecs,
    int stopTimeoutSecs
);
