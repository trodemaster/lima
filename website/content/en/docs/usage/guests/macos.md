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
- Password-less sudo is disabled, except for `/sbin/shutdown -h now`
- Several features are not implemented yet. See [Caveats](#caveats) below.

## Suppressing first-login setup screens

By default, macOS shows a series of setup wizard screens (Setup Assistant /
mini-buddy) on the first GUI login. For automated or headless-style macOS VMs
this is inconvenient. Set `osOpts.darwin.suppressFirstLoginSetup` to have Lima
pre-populate the relevant preference plists during provisioning, before any GUI
session starts, so the setup screens are skipped automatically:

```yaml
osOpts:
  darwin:
    suppressFirstLoginSetup: {}
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
  darwin:
    suppressFirstLoginSetup:
      plist: |
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

When a custom `plist` is supplied, it is used verbatim — no `<build>`/`<version>`
substitution is performed. Copy and adapt the built-in template above, then
supply the actual build and version strings for your target OS release if
needed.

## Clipboard sharing

| ⚡ Requirement | macOS >= 15, on both host and guest |
|-------------------|-----------------------------|

Set `osOpts.darwin.clipboard` to attach a
[SPICE agent port](https://developer.apple.com/documentation/virtualization/vzspiceagentportattachment)
for host-guest clipboard sharing:

```yaml
osOpts:
  darwin:
    clipboard: true
```

This only configures the host side of the channel. A SPICE vdagent-compatible
agent must also be running in the guest — Lima does not ship or install one.
[utmapp/vd_agent](https://github.com/utmapp/vd_agent) provides a macOS build of
such an agent (a `spice-vdagentd` LaunchDaemon plus a per-user `spice-vdagent`
LaunchAgent) that can be built and installed manually.

**Default:** unset — clipboard sharing is disabled.

## Caveats
- No support for turning off the video display.
- No support for automatic port forwarding.
  Use `ssh -L` to manually set up port forwarding, or,
  use the [`vzNAT`](../../config/network/vmnet.md#vznat) network to access the guest by its IP.
- No support for installing custom `caCerts`
