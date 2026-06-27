---
title: macOS
weight: 2
---

| ⚡ Requirement | Lima >= 2.1, macOS, ARM  |
|-------------------|-----------------------------|

Running macOS guests is experimentally supported since Lima v2.1.

{{< tabpane text=true >}}
{{% tab header="macOS only" %}}
```bash
limactl start template:macos
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
| ⚡ Requirement | Lima >= 2.3, macOS >= (ADD VERSION HERE)  |
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

## Pre-populating TCC permissions

macOS uses the Transparency, Consent, and Control (TCC) framework to gate
access to sensitive services (Accessibility, Full Disk Access, etc.). By
default a fresh VM shows consent dialogs on first use, which blocks
unattended or automated setups.

`vmOpts.vz.guestPatch.tccPermissions` lets Lima write rows into the guest's
TCC database during initial disk setup — before the VM boots for the first
time — so permissions are already in place when the guest starts.

### Built-in presets

Presets capture the exact csreq blobs and schema fields that macOS records
when a user manually grants a permission. Use a preset name when the client
is a native macOS or Lima binary:

| Preset | Effect |
|--------|--------|
| `sshd-full-disk-access` | Grants Full Disk Access to `/usr/libexec/sshd-keygen-wrapper` |
| `lima-guestagent-full-disk-access` | Grants Full Disk Access to the Lima guest agent on the cidata volume |
| `terminal-accessibility` | Grants Accessibility + PostEvent to `com.apple.Terminal` |

```yaml
vmOpts:
  vz:
    guestPatch:
      tccPermissions:
        - preset: terminal-accessibility
        - preset: sshd-full-disk-access
```

### Custom entries

For cases not covered by a preset, use the raw fields directly. Note that
custom entries do not include a code-signing requirement (csreq), so they
skip binary identity validation — use presets for Apple-signed system
binaries where an exact csreq is known.

Example — the equivalent of `sshd-full-disk-access` written as a custom entry:

```yaml
vmOpts:
  vz:
    guestPatch:
      tccPermissions:
        - service: kTCCServiceSystemPolicyAllFiles
          client: /usr/libexec/sshd-keygen-wrapper
          clientType: path
          authValue: allow
```

Use the `sshd-full-disk-access` preset instead of the above when possible,
as the preset includes the correct csreq blob that macOS records for
`sshd-keygen-wrapper`.

| Field | Values | Notes |
|-------|--------|-------|
| `service` | TCC service string | e.g. `kTCCServiceAccessibility`, `kTCCServiceSystemPolicyAllFiles` |
| `client` | Bundle ID or path | Bundle ID for Apple-signed apps; absolute path for other binaries |
| `clientType` | `bundle` or `path` | |
| `authValue` | `allow` (default) or `deny` | |

## Caveats
- No support for turning off the video display.
- No support for automatic port forwarding.
  Use `ssh -L` to manually set up port forwarding, or,
  use the [`vzNAT`](../../config/network/vmnet.md#vznat) network to access the guest by its IP.
- No support for installing custom `caCerts`

## Plain mode
containerd and automatic port forwarding are not available on macOS guests regardless
of the mode, so [plain mode](../../config/plain.md) additionally disables only the
host directory mounts.
