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
this is inconvenient. Set `vmOpts.vz.suppressFirstLoginSetup` to have Lima
pre-populate the relevant preference plists during provisioning, before any GUI
session starts, so the setup screens are skipped automatically:

```yaml
vmOpts:
  vz:
    suppressFirstLoginSetup: {}
```

This writes `com.apple.SetupAssistant.plist` into the guest user's home
directory and pre-configures `com.apple.SoftwareUpdate` system preferences so
that the "Update Mac Automatically" dialog is also suppressed. The preferences
are written as root (via the Lima guest agent) before first login, so macOS
reads them as the authoritative initial state and does not reset them.

**Default:** unset — setup screens are shown as normal.

### Custom plist

The built-in `com.apple.SetupAssistant.plist` content stamps the current OS
build/version to prevent macOS from resetting the wizard state. If the keys
that macOS checks change between OS releases, you can supply your own plist:

```yaml
vmOpts:
  vz:
    suppressFirstLoginSetup:
      plist: |
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0">
        <dict>
            <key>DidSeeCloudSetup</key><true/>
            <key>DidSeePrivacy</key><true/>
            <key>SkipExpressSettingsUpdating</key><true/>
        </dict>
        </plist>
```

The plist is written as a file in the cidata ISO and read by the Lima guest
agent before first login. When `plist` is omitted, the built-in template is
used.

## Caveats
- No support for turning off the video display.
- No support for automatic port forwarding.
  Use `ssh -L` to manually set up port forwarding, or,
  use the [`vzNAT`](../../config/network/vmnet.md#vznat) network to access the guest by its IP.
- No support for installing custom `caCerts`
