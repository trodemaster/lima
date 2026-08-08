// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package cidata

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sethvargo/go-password/password"
	"github.com/sirupsen/logrus"

	"github.com/lima-vm/lima/v2/pkg/identifiers"
	"github.com/lima-vm/lima/v2/pkg/iso9660util"
	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/textutil"
	"github.com/lima-vm/lima/v2/pkg/udfutil"
	"github.com/lima-vm/lima/v2/pkg/wimutil"
)

//go:embed cidata.TEMPLATE.d
var templateFS embed.FS

const templateFSRoot = "cidata.TEMPLATE.d"

//go:embed wincidata.TEMPLATE.d
var windowsTemplateFS embed.FS

const windowsTemplateFSRoot = "wincidata.TEMPLATE.d"

type CACerts struct {
	RemoveDefaults *bool
	Trusted        []Cert
}

type Cert struct {
	Lines []string
}

type Containerd struct {
	System  bool
	User    bool
	Archive string
}
type Network struct {
	MACAddress string
	Interface  string
	Metric     uint32
}
type Mount struct {
	Tag        string
	MountPoint string // abs path, accessible by the User
	Type       string
	Options    string
}
type BootCmds struct {
	Lines []string
}

type DataFile struct {
	FileName    string
	Overwrite   string
	Owner       string
	Path        string
	Permissions string
}

type YQProvision struct {
	FileName    string
	Format      string
	Owner       string
	Path        string
	Permissions string
}

type Disk struct {
	Name       string
	Device     string
	Format     bool
	FSType     string
	FSArgs     []string
	Mount      bool
	MountPoint string
}
type TemplateArgs struct {
	Debug                           bool
	OS                              limatype.OS
	Arch                            limatype.Arch
	Name                            string // instance name
	Hostname                        string // instance hostname
	IID                             string // instance id
	User                            string // user name
	Comment                         string // user information
	Home                            string // home directory
	Shell                           string // login shell
	UID                             uint32
	PasswordlessSudo                bool
	SSHPubKeys                      []string
	Mounts                          []Mount
	MountType                       string
	Disks                           []Disk
	GuestInstallPrefix              string
	UpgradePackages                 bool
	Containerd                      Containerd
	Networks                        []Network
	SlirpNICName                    string
	SlirpGateway                    string
	SlirpDNS                        string
	SlirpIPAddress                  string
	UDPDNSLocalPort                 int
	TCPDNSLocalPort                 int
	Env                             map[string]string
	Param                           map[string]string
	BootScripts                     bool
	DataFiles                       []DataFile
	YQProvisions                    []YQProvision
	DNSAddresses                    []string
	CACerts                         CACerts
	HostHomeMountPoint              string
	BootCmds                        []BootCmds
	RosettaEnabled                  bool
	RosettaBinFmt                   bool
	SkipDefaultDependencyResolution bool
	VMType                          string
	VSockPort                       int
	VirtioPort                      string
	Plain                           bool
	TimeZone                        string
	NoCloudInit                     bool
	WindowsInitialPassword          string
	LegacyBIOS                      bool
	IsWindowsServer                 bool
	ImageIndex                      int
	TPM                             bool
	SuppressFirstLoginSetup         bool
	SuppressFirstLoginSetupPlist    string // empty = use built-in plist
	GuestOSVersion                  string // macOS guests only: restore-image OS version, e.g. "27.0.0"; empty otherwise
	GuestOSBuildVersion             string // macOS guests only: restore-image build, e.g. "26A428"; empty otherwise
}

func (t *TemplateArgs) generateWindowsInitialPassword() error {
	const pwLen = 16
	// Avoid special characters to minimize potential keyboard layout issue.
	pw, err := password.Generate(pwLen, pwLen/4, 0, false, false)
	if err != nil {
		return fmt.Errorf("failed to generate password: %w", err)
	}

	t.WindowsInitialPassword = pw
	return nil
}

// windowsImageCandidates are the installer image paths checked, in order,
// for a Windows install image inside the installer ISO.
var windowsImageCandidates = []string{"sources/install.wim", "sources/install.esd"}

// checkWindowsVersion determines whether the guest is Windows 11 (client) or
// Windows Server, and which WIM/ESD image index to install, by reading the
// installer ISO's own image list -- rather than trusting the ISO's volume
// label, which varies across distribution channels (retail, volume
// license, Insider, UUP) and is not a reliable signal for either fact.
func (t *TemplateArgs) checkWindowsVersion(instDir, edition, installationType string) error {
	imagePath := filepath.Join(instDir, filenames.ISO)
	f, err := os.Open(imagePath)
	if err != nil {
		return fmt.Errorf("failed to open %#q: %w", imagePath, err)
	}
	defer f.Close()

	var images []wimutil.Image
	var openErrs []error
	for _, name := range windowsImageCandidates {
		r, err := udfutil.Open(f, name)
		if err != nil {
			openErrs = append(openErrs, err)
			continue
		}
		images, err = wimutil.Images(r)
		if err != nil {
			return fmt.Errorf("failed to read the image list from %#q on %#q: %w", name, imagePath, err)
		}
		break
	}
	if images == nil {
		return fmt.Errorf("failed to find a Windows install image (%s) on %#q: %w",
			strings.Join(windowsImageCandidates, " or "), imagePath, errors.Join(openErrs...))
	}

	isServer := images[0].IsServer()
	for _, img := range images[1:] {
		if img.IsServer() != isServer {
			return fmt.Errorf("%#q contains a mix of Windows client and server images, which is not supported", imagePath)
		}
	}
	t.IsWindowsServer = isServer

	selected, err := selectWindowsImage(images, edition, installationType)
	if err != nil {
		return fmt.Errorf("%#q: %w", imagePath, err)
	}
	t.ImageIndex = selected.Index

	return nil
}

// describeImage formats an image for logging: its EDITIONID, its
// INSTALLATIONTYPE, and its DISPLAYNAME (the WIM's own human-readable name,
// e.g. "Windows Server 2025 Standard Evaluation (Desktop Experience)"),
// which is otherwise easy to lose track of once several images share the
// same EDITIONID.
func describeImage(img wimutil.Image) string {
	return fmt.Sprintf("%#q (installationType=%#q, display=%#q)", img.EditionID, img.InstallationType, img.DisplayName)
}

func describeImages(images []wimutil.Image) string {
	described := make([]string, len(images))
	for i, img := range images {
		described[i] = describeImage(img)
	}
	return strings.Join(described, ", ")
}

// selectWindowsImage picks the WIM/ESD image to install.
//
// If edition is set, it must match (case-insensitively) at least one
// image's EDITIONID, or selectWindowsImage returns an error listing every
// image found. EDITIONID is not always unique: on Windows Server ISOs, the
// Core and Desktop Experience variants of the same edition commonly share
// one. If installationType is also set, it further narrows by
// INSTALLATIONTYPE (e.g. "Server" vs. "Server Core") the same way.
//
// If edition is unset entirely, an image with EDITIONID "Professional" is
// preferred -- matching what the hardcoded index this behavior replaces
// resolved to on the standard single-edition-per-arch retail ISO -- falling
// back to the first image if none is found.
//
// If edition narrowed the candidates to more than one image and
// installationType wasn't set to narrow further, an installation type that
// doesn't look like a "Core" variant is preferred, since that's the more
// broadly usable default for someone who didn't ask for Core specifically.
// Either fallback is logged, listing every candidate that was available, so
// picking a default is never silent.
func selectWindowsImage(images []wimutil.Image, edition, installationType string) (wimutil.Image, error) {
	candidates := images
	if edition != "" {
		var matched []wimutil.Image
		for _, img := range images {
			if strings.EqualFold(img.EditionID, edition) {
				matched = append(matched, img)
			}
		}
		if len(matched) == 0 {
			return wimutil.Image{}, fmt.Errorf("no image with edition %#q found (set `osOpts.windows.edition` to one of: %s)", edition, describeImages(images))
		}
		candidates = matched
	}

	if installationType != "" {
		var matched []wimutil.Image
		for _, img := range candidates {
			if strings.EqualFold(img.InstallationType, installationType) {
				matched = append(matched, img)
			}
		}
		if len(matched) == 0 {
			return wimutil.Image{}, fmt.Errorf("no image with edition %#q and installation type %#q found (available: %s)", edition, installationType, describeImages(candidates))
		}
		candidates = matched
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}

	if edition == "" {
		for _, img := range candidates {
			if strings.EqualFold(img.EditionID, "Professional") {
				return img, nil
			}
		}
		logrus.Warnf("no %#q edition found among: %s; defaulting to %s. Set `osOpts.windows.edition` to choose a specific one.",
			"Professional", describeImages(candidates), describeImage(candidates[0]))
		return candidates[0], nil
	}

	for _, img := range candidates {
		if !strings.Contains(strings.ToLower(img.InstallationType), "core") {
			logrus.Warnf("edition %#q matches multiple images: %s; defaulting to %s. Set `osOpts.windows.installationType` to choose a different one.",
				edition, describeImages(candidates), describeImage(img))
			return img, nil
		}
	}
	logrus.Warnf("edition %#q matches multiple images: %s; defaulting to %s. Set `osOpts.windows.installationType` to choose a different one.",
		edition, describeImages(candidates), describeImage(candidates[0]))
	return candidates[0], nil
}

func ValidateTemplateArgs(args *TemplateArgs) error {
	if err := identifiers.Validate(args.Name); err != nil {
		return err
	}
	// args.User is intentionally not validated here; the user can override with any name they want
	// limayaml.FillDefault will validate the default (local) username, but not an explicit setting
	if args.User == "root" {
		return errors.New("field User must not be `root`")
	}
	if args.UID == 0 {
		return errors.New("field UID must not be 0")
	}
	if args.Home == "" {
		return errors.New("field Home must be set")
	}
	if args.Shell == "" {
		return errors.New("field Shell must be set")
	}
	if len(args.SSHPubKeys) == 0 {
		return errors.New("field SSHPubKeys must be set")
	}
	for i, m := range args.Mounts {
		f := m.MountPoint
		if !path.IsAbs(f) {
			return fmt.Errorf("field mounts[%d] must be absolute, got %#q", i, f)
		}
	}
	return nil
}

func ExecuteTemplateCloudConfig(args *TemplateArgs) ([]byte, error) {
	if err := ValidateTemplateArgs(args); err != nil {
		return nil, err
	}

	userData, err := templateFS.ReadFile(path.Join(templateFSRoot, "user-data"))
	if err != nil {
		return nil, err
	}

	cloudConfigYaml := string(userData)
	return textutil.ExecuteTemplate(cloudConfigYaml, args)
}

func executeTemplateWalkDir(args *TemplateArgs, fsys fs.FS, root string) ([]iso9660util.Entry, error) {
	if err := ValidateTemplateArgs(args); err != nil {
		return nil, err
	}

	fsys, err := fs.Sub(fsys, root)
	if err != nil {
		return nil, err
	}

	var layout []iso9660util.Entry
	walkFn := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("got non-regular file %#q", path)
		}
		templateB, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		b, err := textutil.ExecuteTemplate(string(templateB), args)
		if err != nil {
			return err
		}
		layout = append(layout, iso9660util.Entry{
			Path:   path,
			Reader: bytes.NewReader(b),
		})
		return nil
	}

	if err := fs.WalkDir(fsys, ".", walkFn); err != nil {
		return nil, err
	}

	return layout, nil
}

func ExecuteTemplateCIDataISO(args *TemplateArgs) ([]iso9660util.Entry, error) {
	return executeTemplateWalkDir(args, templateFS, templateFSRoot)
}

func ExecuteTemplateWindowsISO(args *TemplateArgs) ([]iso9660util.Entry, error) {
	return executeTemplateWalkDir(args, windowsTemplateFS, windowsTemplateFSRoot)
}
