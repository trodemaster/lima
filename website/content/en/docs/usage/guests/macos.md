---
title: macOS
weight: 2
---

| ⚡ Requirement | Lima >= 2.1, macOS (same or newer than the guest OS), ARM  |
|-------------------|-----------------------------|

Running macOS guests is experimentally supported since Lima v2.1.

{{< tabpane text=true >}}
{{% tab header="macOS only" %}}
```bash
limactl start template:macos
```

To specify the macOS version explicitly:
```bash
limactl start template:macos-27
```
{{% /tab %}}
{{% tab header="With Homebrew" %}}
```bash
limactl start template:homebrew-macos
```
{{% /tab %}}
{{< /tabpane >}}

The user password is randomly generated and stored in the `~/password` file in the VM.
Consider changing it after the first login.

```bash
limactl shell macos cat /Users/${USER}.guest/password
```

## Difference from Linux guests
- Password login is enabled
- Password-less sudo is disabled, except for `/sbin/shutdown -h now` (see [Sudo](/docs/config/sudo/) — this is not currently configurable on macOS)
- Several features are not implemented yet. See [Caveats](#caveats) below.

## Advanced topics
### Suppressing first-login setup screens
| ⚡ Requirement | Lima >= 2.3, macOS >= 13.0  |
|-------------------|-----------------------------|

By default, macOS shows a series of setup wizard screens (Setup Assistant /
mini-buddy) on the first GUI login. For automated or headless-style macOS VMs
this is inconvenient. Set `osOpts.Darwin.suppressFirstLoginSetup` to have Lima
pre-populate the relevant preference plists during provisioning, before any GUI
session starts, so the setup screens are skipped automatically:

```yaml
osOpts:
  Darwin:
    suppressFirstLoginSetup: true
```

This writes `com.apple.SetupAssistant.plist` into the guest user's home
directory and pre-configures `com.apple.SoftwareUpdate` system preferences so
that the "Update Mac Automatically" dialog is also suppressed. The preferences
are written as root (via the Lima guest agent) before first login, so macOS
reads them as the authoritative initial state and does not reset them.

**Default:** unset — setup screens are shown as normal.

### Custom plist

The built-in `com.apple.SetupAssistant.plist` template is shown below. At VM
creation time, `<build>` is replaced with the output of `sw_vers -buildVersion`
and `<version>` with `sw_vers -productVersion` from inside the guest. The
version stamps are what macOS checks to decide whether setup is already
complete — without them macOS resets `MiniBuddyLaunchReason` to 13 on first
GUI login.

```yaml
osOpts:
  Darwin:
    suppressFirstLoginSetup: true
    suppressFirstLoginSetupPlist: |
      <?xml version="1.0" encoding="UTF-8"?>
      <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
      <plist version="1.0">
      <dict>
      	<key>DidSeeAccessibility</key><true/>
      	<key>DidSeeActivationLock</key><true/>
      	<key>DidSeeAppStore</key><true/>
      	<key>DidSeeAppearanceSetup</key><true/>
      	<key>DidSeeApplePaySetup</key><true/>
      	<key>DidSeeCloudSetup</key><true/>
      	<key>DidSeeLockdownMode</key><true/>
      	<key>DidSeePrivacy</key><true/>
      	<key>DidSeeScreenTime</key><true/>
      	<key>DidSeeSetupSequence</key><true/>
      	<key>DidSeeSiriSetup</key><true/>
      	<key>DidSeeSyncSetup</key><true/>
      	<key>DidSeeSyncSetup2</key><true/>
      	<key>DidSeeTermsOfAddress</key><true/>
      	<key>DidSeeTouchIDSetup</key><true/>
      	<key>DidSeeiCloudLoginForStorageServices</key><true/>
      	<key>LastPreLoginTasksPerformedBuild</key><string><build></string>
      	<key>LastPreLoginTasksPerformedVersion</key><string><version></string>
      	<key>LastSeenAgeRangeSelectionProductVersion</key><string><version></string>
      	<key>LastSeenBuddyBuildVersion</key><string><build></string>
      	<key>LastSeenCloudProductVersion</key><string><version></string>
      	<key>LastSeenDiagnosticsProductVersion</key><string><version></string>
      	<key>MiniBuddyLaunchReason</key><integer>0</integer>
      	<key>MiniBuddyShouldLaunchToResumeSetup</key><false/>
      	<key>SkipExpressSettingsUpdating</key><true/>
      	<key>SkipFirstLoginOptimization</key><true/>
      </dict>
      </plist>
```

When `suppressFirstLoginSetupPlist` is supplied, it is used verbatim — no
`<build>`/`<version>` substitution is performed. Copy and adapt the built-in
template above, then supply the actual build and version strings for your
target OS release if needed.

### Guest provisioning
| ⚡ Requirement | Lima >= 2.3, macOS >= 27.0 on both host and guest, ARM |
|-------------------|-----------------------------|

On macOS 27 and later, the Virtualization framework can create the guest user
account on the first boot after the OS is installed. Set
`osOpts.Darwin.guestProvisioning` to have Lima pass these settings to that
first boot:

```yaml
osOpts:
  Darwin:
    guestProvisioning:
      password: "..."
      logsInAutomatically: true
      enablesRemoteLogin: true
```

The account is the Lima user: `user.comment` is its full name and `user.name` its
username, which defaults to your host username followed by `.guest`, like the home
directory Lima uses for other macOS guests, so that it cannot clash with your host
home directory when that is mounted in the guest.
Setup Assistant creates the home directory at `/Users/<user.name>`, so
`user.home` must be left at that value.

macOS only reads these settings on the first boot after installation, so they
have no effect on an existing instance. Guests older than macOS 27 ignore them,
so Lima checks the guest version recorded at install time and skips
provisioning with a warning when the guest is older than macOS 27, or when the
host is older than macOS 27.

The password is stored in plain text in the instance's `lima.yaml`.

**Default:** unset — no guest provisioning.

## Caveats
- No support for turning off the video display.
- No support for automatic port forwarding.
  Use `ssh -L` to manually set up port forwarding, or,
  use the [`vzNAT`]({{< ref "/docs/config/network/vznat" >}}) network to access the guest by its IP.
- No support for installing custom `caCerts`

## Plain mode
containerd and automatic port forwarding are not available on macOS guests regardless
of the mode, so [plain mode]({{< ref "/docs/config/plain" >}}) additionally disables only the
host directory mounts.
