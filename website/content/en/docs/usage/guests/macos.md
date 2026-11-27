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

### Pre-populating TCC permissions
| ⚡ Requirement | Lima >= 2.4 |
|-------------------|-----------------------------|

macOS uses the Transparency, Consent, and Control (TCC) framework to gate
access to sensitive services such as Full Disk Access. Some of these can
only be granted in System Settings, for example "Allow full disk access for
remote users", which blocks unattended or automated setups.

`osOpts.Darwin.tccPermissions` lets Lima write entries into the guest's
system TCC database during initial disk setup, before the VM boots for the
first time, so the permissions are already in place when the guest starts.
The entries are written once, when the disk is created; changing them later
has no effect on an existing instance.

| Preset | Effect |
|--------|--------|
| `sshd-full-disk-access` | Full Disk Access for SSH sessions (`/usr/libexec/sshd-keygen-wrapper`), as "Allow full disk access for remote users" in System Settings |
| `lima-guestagent-full-disk-access` | Full Disk Access for the Lima guest agent on the cidata volume |

```yaml
osOpts:
  Darwin:
    tccPermissions:
      - preset: sshd-full-disk-access
```

Only these presets are supported. macOS keeps every entry written this way
but doesn't honor all of them (for example, an Accessibility entry for
Terminal has no effect on macOS 26 and 27 guests), so Lima only offers
entries that are known to work.

### Automatic login
| ⚡ Requirement | Lima >= 2.4, macOS >= 13.0 |
|-------------------|-----------------------------|

By default the guest boots to the login window. Set `osOpts.Darwin.autoLogin` to
have Lima turn on automatic login for the Lima user when it creates the account,
so that GUI sessions (for example a CI runner that needs the window server) start
without anyone logging in:

```yaml
osOpts:
  Darwin:
    autoLogin: true
```

macOS reads the setting when the login window starts, so automatic login takes
effect from the second boot of a new instance. It stores a copy of the password
Lima generated (`~/password`), so changing the account password breaks automatic
login; turn it on again in System Settings > Users & Groups afterwards. Automatic
login is not available while FileVault is on.

**Default:** `false`

### Guest provisioning
| ⚡ Requirement | Lima >= 2.4, macOS >= 27.0 on both host and guest |
|-------------------|-----------------------------|

When both the host and the guest run macOS 27 or later, Lima creates the guest
user with the Virtualization framework's guest provisioning
([`VZMacGuestProvisioningOptions`](https://developer.apple.com/documentation/virtualization/vzmacguestprovisioningoptions))
instead of `sysadminctl`: on the first boot after installation, Setup Assistant
creates the account from the Lima user (`user.name`, with `user.comment` as the
full name), and Lima then sets it up as usual (SSH keys, `~/password`, sudo).
Nothing needs to be configured, and older guests are set up as before.

Setup Assistant always creates the home directory at `/Users/<user.name>`, so on
the first boot the guest moves it to `user.home` (`/Users/<user.name>.guest` by
default, which cannot clash with the host home directory mounted in the guest)
before anyone logs in. macOS only lets a process with Full Disk Access move a
home directory, so Lima adds the
[`lima-guestagent-full-disk-access`](#pre-populating-tcc-permissions) TCC permission for these
guests. [`osOpts.Darwin.autoLogin`](#automatic-login) is turned on after the move,
as for older guests.

macOS only applies guest provisioning on the first boot after installation, so it
has no effect on existing instances. If that boot is interrupted after Setup
Assistant has created the account, the next boot moves the home and finishes the
setup. If it is interrupted before, delete the instance and create it again.

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
