// macos_guest_provisioning_darwin_arm64.m
// VZMacGuestProvisioningOptions first-boot provisioning for macOS 27+ guests.
//
// VZMacGuestProvisioningOptions (macOS 27+) lets the host provide user account
// details — fullName, username, password, autologin, remote login — that macOS
// applies automatically during the first boot after a restore.
//
// Integration: Lima runs a transient "provisioning boot" after the OS install
// (DFU or VZMacOSInstaller) completes. The provisioning VM starts with these
// options, macOS configures the account, then we request a stop. Lima's normal
// Start() becomes the clean second boot with the account already in place.
//
// Runtime ObjC dispatch is used throughout so this file compiles against any
// macOS SDK. guestProvisioningAvailable() returns 0 on pre-macOS 27 hosts.
//
// Overlaps with configure.sh:
//   - password       → configure.sh MACOS_PASSWORD
//   - autologin      → configure.sh AUTO_LOGIN + kcpassword
//   - remote login   → configure.sh SSH_PUBLIC_KEY injection
// configure.sh still handles SSH key injection, chezmoi, energy-saver settings,
// setup assistant suppression, and wallpaper — none of which are covered here.
// TODO: Remove overlapping configure.sh steps once this stabilises on macOS 27.

#pragma clang diagnostic ignored "-Wunguarded-availability-new"

#import <Foundation/Foundation.h>
#import <Virtualization/Virtualization.h>
#include <dispatch/dispatch.h>
#include <objc/message.h>
#include <objc/runtime.h>

#import "macos_guest_provisioning_darwin_arm64.h"

// ──────────────────────────────────────────────────────────────────────────────
// Availability
// ──────────────────────────────────────────────────────────────────────────────

int guestProvisioningAvailable(void) {
    return objc_getClass("VZMacGuestProvisioningOptions") != NULL ? 1 : 0;
}

// ──────────────────────────────────────────────────────────────────────────────
// Provisioning options (via runtime ObjC — no SDK 27 header required)
// ──────────────────────────────────────────────────────────────────────────────

// Creates a VZMacGuestProvisioningOptions instance with the given fields,
// or returns nil (with *outError set) if the class is unavailable or any
// property is invalid.
static id buildProvisioningOptions(const char *fullName,
                                   const char *username,
                                   const char *password,
                                   bool logsInAutomatically,
                                   bool enablesRemoteLogin,
                                   NSString **outError) {
    Class cls = objc_getClass("VZMacGuestProvisioningOptions");
    if (!cls) {
        if (outError) *outError = @"VZMacGuestProvisioningOptions is not available on this host (requires macOS 27+)";
        return nil;
    }

    id opts = [[cls alloc] init];
    if (!opts) {
        if (outError) *outError = @"VZMacGuestProvisioningOptions alloc/init returned nil";
        return nil;
    }

    // Set properties via KVC — safe on any ObjC object without importing the header.
    @try {
        [opts setValue:[NSString stringWithUTF8String:fullName]  forKey:@"fullName"];
        [opts setValue:[NSString stringWithUTF8String:username]  forKey:@"username"];
        [opts setValue:[NSString stringWithUTF8String:password]  forKey:@"password"];
        [opts setValue:@(logsInAutomatically) forKey:@"logsInAutomatically"];
        [opts setValue:@(enablesRemoteLogin)  forKey:@"enablesRemoteLogin"];
    } @catch (NSException *ex) {
        if (outError)
            *outError = [NSString stringWithFormat:@"failed to set provisioning option: %@", ex.reason];
        return nil;
    }

    // Validate — calls VZGuestProvisioningOptions -validateWithError:
    SEL validateSel = NSSelectorFromString(@"validateWithError:");
    if ([opts respondsToSelector:validateSel]) {
        NSError *valErr = nil;
        BOOL ok = ((BOOL (*)(id, SEL, NSError **))objc_msgSend)(opts, validateSel, &valErr);
        if (!ok) {
            if (outError)
                *outError = [NSString stringWithFormat:@"provisioning options invalid: %@",
                             valErr ? valErr.localizedDescription : @"(no detail)"];
            return nil;
        }
    }

    return opts;
}

// ──────────────────────────────────────────────────────────────────────────────
// Minimal VM for the provisioning boot
//
// We need: boot loader, platform (machine ID + hw model + aux storage), disk,
// a graphics device, and input devices. Network is not required for provisioning
// — macOS configures the account locally.
// ──────────────────────────────────────────────────────────────────────────────

static VZVirtualMachine *buildProvisioningVM(NSString *instDir,
                                              unsigned int cpuCount,
                                              unsigned long long memorySizeBytes,
                                              dispatch_queue_t *outQueue,
                                              NSString **outError) {
    NSError *err = nil;

    VZMacOSBootLoader *bootLoader = [[VZMacOSBootLoader alloc] init];

    // Machine identifier
    NSString *identPath = [instDir stringByAppendingPathComponent:@"vz-identifier"];
    NSData *identData = [NSData dataWithContentsOfFile:identPath];
    if (!identData) {
        if (outError) *outError = [NSString stringWithFormat:@"could not read %@", identPath];
        return nil;
    }
    VZMacMachineIdentifier *machineID =
        [[VZMacMachineIdentifier alloc] initWithDataRepresentation:identData];
    if (!machineID) {
        if (outError) *outError = @"VZMacMachineIdentifier initWithDataRepresentation failed";
        return nil;
    }

    // Hardware model
    NSString *hwModelPath = [instDir stringByAppendingPathComponent:@"vz-hwmodel"];
    NSData *hwModelData = [NSData dataWithContentsOfFile:hwModelPath];
    if (!hwModelData) {
        if (outError) *outError = [NSString stringWithFormat:@"could not read %@", hwModelPath];
        return nil;
    }
    VZMacHardwareModel *hwModel =
        [[VZMacHardwareModel alloc] initWithDataRepresentation:hwModelData];
    if (!hwModel) {
        if (outError) *outError = @"VZMacHardwareModel initWithDataRepresentation failed";
        return nil;
    }

    // Auxiliary storage (create if absent)
    NSString *auxPath = [instDir stringByAppendingPathComponent:@"vz-aux"];
    NSURL *auxURL = [NSURL fileURLWithPath:auxPath];
    VZMacAuxiliaryStorage *aux;
    if ([[NSFileManager defaultManager] fileExistsAtPath:auxPath]) {
        aux = [[VZMacAuxiliaryStorage alloc] initWithURL:auxURL];
    } else {
        aux = [[VZMacAuxiliaryStorage alloc]
               initCreatingStorageAtURL:auxURL
               hardwareModel:hwModel
               options:VZMacAuxiliaryStorageInitializationOptionAllowOverwrite
               error:&err];
        if (!aux) {
            if (outError)
                *outError = [NSString stringWithFormat:@"VZMacAuxiliaryStorage init failed: %@",
                             err.localizedDescription];
            return nil;
        }
    }

    // Platform
    VZMacPlatformConfiguration *platform = [[VZMacPlatformConfiguration alloc] init];
    platform.machineIdentifier = machineID;
    platform.hardwareModel     = hwModel;
    platform.auxiliaryStorage  = aux;

    // Primary disk
    NSString *diskPath = [instDir stringByAppendingPathComponent:@"disk"];
    NSURL *diskURL = [NSURL fileURLWithPath:diskPath];
    VZDiskImageStorageDeviceAttachment *diskAttach =
        [[VZDiskImageStorageDeviceAttachment alloc] initWithURL:diskURL readOnly:NO error:&err];
    if (!diskAttach) {
        if (outError)
            *outError = [NSString stringWithFormat:@"VZDiskImageStorageDeviceAttachment failed: %@",
                         err.localizedDescription];
        return nil;
    }
    VZVirtioBlockDeviceConfiguration *diskCfg =
        [[VZVirtioBlockDeviceConfiguration alloc] initWithAttachment:diskAttach];

    // Display — macOS requires a graphics device
    VZMacGraphicsDeviceConfiguration *graphics = [[VZMacGraphicsDeviceConfiguration alloc] init];
    graphics.displays = @[[[VZMacGraphicsDisplayConfiguration alloc]
                            initWithWidthInPixels:1920 heightInPixels:1200 pixelsPerInch:80]];

    // Input devices — macOS requires keyboard + pointing
    VZMacKeyboardConfiguration *keyboard = [[VZMacKeyboardConfiguration alloc] init];
    VZMacTrackpadConfiguration *trackpad = [[VZMacTrackpadConfiguration alloc] init];

    // Assemble
    VZVirtualMachineConfiguration *config = [[VZVirtualMachineConfiguration alloc] init];
    config.bootLoader      = bootLoader;
    config.platform        = platform;
    config.CPUCount        = cpuCount;
    config.memorySize      = memorySizeBytes;
    config.storageDevices  = @[diskCfg];
    config.graphicsDevices = @[graphics];
    config.keyboards       = @[keyboard];
    config.pointingDevices = @[trackpad];

    if (![config validateWithError:&err]) {
        if (outError)
            *outError = [NSString stringWithFormat:@"VZVirtualMachineConfiguration validation failed: %@",
                         err.localizedDescription];
        return nil;
    }

    dispatch_queue_t queue = dispatch_queue_create(
        "com.lima.guest-provisioning",
        dispatch_queue_attr_make_with_qos_class(NULL, QOS_CLASS_USER_INTERACTIVE, 0));
    if (outQueue) *outQueue = queue;
    return [[VZVirtualMachine alloc] initWithConfiguration:config queue:queue];
}

// ──────────────────────────────────────────────────────────────────────────────
// Helper: read current VM state on the VM queue
// ──────────────────────────────────────────────────────────────────────────────

static VZVirtualMachineState vmState(VZVirtualMachine *vm, dispatch_queue_t q) {
    __block VZVirtualMachineState s = VZVirtualMachineStateStopped;
    dispatch_sync(q, ^{ s = [vm state]; });
    return s;
}

// ──────────────────────────────────────────────────────────────────────────────
// Public entry point
// ──────────────────────────────────────────────────────────────────────────────

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
    int stopTimeoutSecs)
{
    @autoreleasepool {
        NSString *dir    = [NSString stringWithUTF8String:instDir];
        NSString *errMsg = nil;

        // 1. Check runtime availability.
        if (!guestProvisioningAvailable()) {
            return strdup("VZMacGuestProvisioningOptions requires macOS 27+ — skipping provisioning boot");
        }

        // 2. Build provisioning options.
        id provOpts = buildProvisioningOptions(fullName, username, password,
                                               logsInAutomatically, enablesRemoteLogin,
                                               &errMsg);
        if (!provOpts) goto fail;

        // 3. Build the VM.
        {
            dispatch_queue_t vmQueue = NULL;
            VZVirtualMachine *vm = buildProvisioningVM(dir, cpuCount, memorySizeBytes,
                                                        &vmQueue, &errMsg);
            if (!vm) goto fail;

            // 4. Attach provisioning options to VZMacOSVirtualMachineStartOptions.
            VZMacOSVirtualMachineStartOptions *startOpts =
                [[VZMacOSVirtualMachineStartOptions alloc] init];

            SEL setProvSel = NSSelectorFromString(@"setGuestProvisioningOptions:error:");
            if (![startOpts respondsToSelector:setProvSel]) {
                errMsg = @"VZMacOSVirtualMachineStartOptions does not respond to "
                         @"setGuestProvisioningOptions:error: — requires macOS 27+";
                goto fail;
            }
            NSError *setErr = nil;
            BOOL setOK = ((BOOL (*)(id, SEL, id, NSError **))objc_msgSend)(
                startOpts, setProvSel, provOpts, &setErr);
            if (!setOK) {
                errMsg = [NSString stringWithFormat:@"setGuestProvisioningOptions:error: failed: %@",
                          setErr ? setErr.localizedDescription : @"(no detail)"];
                goto fail;
            }

            // 5. Start the VM with provisioning options.
            fprintf(stderr, "guest-provisioning: starting VM (first boot after restore)\n");
            __block NSError *startErr = nil;
            dispatch_semaphore_t startSema = dispatch_semaphore_create(0);
            dispatch_sync(vmQueue, ^{
                [vm startWithOptions:startOpts completionHandler:^(NSError *e) {
                    startErr = e;
                    dispatch_semaphore_signal(startSema);
                }];
            });

            dispatch_time_t bootDeadline =
                dispatch_time(DISPATCH_TIME_NOW, (int64_t)bootTimeoutSecs * NSEC_PER_SEC);
            if (dispatch_semaphore_wait(startSema, bootDeadline) != 0) {
                // No completion callback within timeout — this shouldn't happen.
                errMsg = [NSString stringWithFormat:
                          @"VM start did not complete within %ds", bootTimeoutSecs];
                goto fail;
            }
            if (startErr) {
                errMsg = [NSString stringWithFormat:@"VM start failed: %@",
                          startErr.localizedDescription];
                goto fail;
            }
            fprintf(stderr, "guest-provisioning: VM running — waiting up to %ds for macOS first-boot setup\n",
                    provisionTimeoutSecs);

            // 6. Wait for macOS first-boot setup to complete.
            //    macOS applies provisioning options early in the boot sequence; we wait a
            //    fixed interval to ensure the guest finishes writing its config before we
            //    stop it. The VM will NOT stop itself after provisioning.
            time_t provDeadline = time(NULL) + provisionTimeoutSecs;
            while (time(NULL) < provDeadline) {
                VZVirtualMachineState state = vmState(vm, vmQueue);
                if (state == VZVirtualMachineStateStopped ||
                    state == VZVirtualMachineStateError) {
                    fprintf(stderr, "guest-provisioning: VM stopped early (state=%d)\n", (int)state);
                    break;
                }
                usleep(2000000); // 2s poll
            }

            // 7. Request graceful stop.
            VZVirtualMachineState stateNow = vmState(vm, vmQueue);
            if (stateNow != VZVirtualMachineStateStopped &&
                stateNow != VZVirtualMachineStateError) {
                fprintf(stderr, "guest-provisioning: requesting stop\n");
                dispatch_sync(vmQueue, ^{
                    NSError *stopErr = nil;
                    BOOL requested = [vm requestStopWithError:&stopErr];
                    if (!requested)
                        fprintf(stderr, "guest-provisioning: requestStop failed (%s), will force-stop\n",
                                stopErr.localizedDescription.UTF8String ?: "?");
                });

                // 8. Wait up to stopTimeoutSecs for the VM to stop.
                time_t stopDeadline = time(NULL) + stopTimeoutSecs;
                while (time(NULL) < stopDeadline) {
                    VZVirtualMachineState s = vmState(vm, vmQueue);
                    if (s == VZVirtualMachineStateStopped || s == VZVirtualMachineStateError) break;
                    usleep(1000000); // 1s
                }

                // 9. Force-stop if still running.
                if (vmState(vm, vmQueue) != VZVirtualMachineStateStopped) {
                    fprintf(stderr, "guest-provisioning: force-stopping VM\n");
                    dispatch_semaphore_t forceSema = dispatch_semaphore_create(0);
                    dispatch_sync(vmQueue, ^{
                        [vm stopWithCompletionHandler:^(NSError *e) {
                            if (e) fprintf(stderr, "guest-provisioning: force-stop error: %s\n",
                                           e.localizedDescription.UTF8String ?: "?");
                            dispatch_semaphore_signal(forceSema);
                        }];
                    });
                    dispatch_semaphore_wait(forceSema,
                        dispatch_time(DISPATCH_TIME_NOW, 30LL * NSEC_PER_SEC));
                }
            }

            fprintf(stderr, "guest-provisioning: provisioning boot complete\n");
            return NULL; // success
        }

    fail:
        if (errMsg) return strdup(errMsg.UTF8String);
        return strdup("unknown guest provisioning error");
    }
}
