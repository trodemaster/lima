---
title: Windows
weight: 2
---

| ⚡ Requirement | Lima >= 2.2, QEMU, swtpm |
|-------------------|-----------------------------|

Running Windows guests is experimentally supported since Lima v2.2.

{{< tabpane text=true >}}
{{% tab header="Windows 11" %}}
```
limactl start template:windows
```
{{% /tab %}}
{{% tab header="Windows server 2025" %}}
```
limactl start template:windows-2025
```
{{% /tab %}}
{{< /tabpane >}}

The user password is randomly generated and stored in the `%USERPROFILE%\password.txt` file in the VM.
Consider changing it after the first login.

By default, Windows 11 enables Trusted Platform Module (TPM) emulation because of the hardware requirement. However, you can turn it off (in that case, lima bypasses the hardware check). In order to use TPM emulation, you need to install `swtpm` on your host computer.

For Windows server 2025, TPM emulation is disabled by default. However, there are some benefits if you enable TPM emulation. For example, you can install [BitLocker disk encryption](https://learn.microsoft.com/en-us/windows/security/operating-system-security/data-protection/bitlocker/install-server) on your VM.

## Selecting an edition

Windows installer ISOs and their `sources/install.wim` (or `sources/install.esd`) commonly contain more than one edition to choose from. Lima reads the image's own edition list directly from the ISO and installs from it, rather than trusting the ISO's volume label (which varies across distribution channels and isn't a reliable signal).

By default, Lima installs the `Professional` edition if the ISO offers one; otherwise it installs whichever edition is listed first. On the Windows Server 2025 Evaluation ISO that `template:windows-2025` downloads, that means Windows Server 2025 Standard (Core). To choose a specific edition, set `osOpts.windows.edition` to its `EDITIONID` (matched case-insensitively):

```yaml
osOpts:
  windows:
    edition: "Enterprise"
```

The exact set of available editions depends on which ISO you're using, but common `EDITIONID` values include:

| `EDITIONID`                  | Typical display name                          |
|-------------------------------|-------------------------------------------------|
| `Core`                         | Windows Home                                     |
| `Professional`                 | Windows Pro                                      |
| `Enterprise`                    | Windows Enterprise                               |
| `Education`                     | Windows Education                                |
| `ProfessionalEducation`         | Windows Pro Education                            |
| `ProfessionalWorkstation`       | Windows Pro for Workstations                     |
| `ProfessionalSingleLanguage`    | Windows Pro Single Language                      |
| `IoTEnterprise`                 | Windows IoT Enterprise                           |
| `CloudEdition`                  | Windows SE                                       |
| `ServerStandardEval`            | Windows Server 2025 Standard Evaluation          |
| `ServerDatacenterEval`          | Windows Server 2025 Datacenter Evaluation        |

If `edition` doesn't match any image on the ISO, or you want to see what's actually available on the ISO you downloaded, set it to anything and Lima will fail with an error listing every image it found, by `EDITIONID`, installation type, and display name.

### Disambiguating Core vs. Desktop Experience

`EDITIONID` is not always unique per image: on the Windows Server 2025 ISO, the Core and Desktop Experience variants of the same edition (e.g. both Standard images) share one `EDITIONID`, distinguished only by installation type. If `edition` matches more than one image this way, Lima logs a warning listing every match and installs whichever one isn't a "Core" variant, since that's the more broadly usable default for someone who didn't ask for Core specifically. Set `osOpts.windows.installationType` to the image's own installation type (e.g. `"Server Core"` or `"Server"`, matched case-insensitively) to pick explicitly instead:

```yaml
osOpts:
  windows:
    edition: "ServerStandardEval"
    installationType: "Server Core"
```

## Difference from Linux guests
- Several features are not implemented yet. See [Caveats](#caveats) below.

## Caveats
- For Windows 11 guest, you need to download the installer ISO manually from [here](https://www.microsoft.com/en-us/software-download/windows11)
- QEMU is the only VM driver that supports Windows guests
- provision feature is limited support (no `boot`, `yq` modes, and `data` and `dependency` modes have limitations)
- Only plain mode is supported (no file mount, no dynamic port-forwarding)
- Booting Windows 11 may occasionally fail. If it fails, please delete the instance and try it again from scratch.
