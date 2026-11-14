// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

// Package fakecloudinit is a fake cloud-init implementation for macOS.
//
// TODO: support other OS that does not have cloud-init.
package fakecloudinit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/sethvargo/go-password/password"
	"github.com/sirupsen/logrus"

	"github.com/lima-vm/lima/v2/pkg/cidata/cloudinittypes"
	"github.com/lima-vm/lima/v2/pkg/osutil"
)

func Run(ctx context.Context) error {
	const mnt = "/Volumes/cidata"
	var errs []error
	if err := enableSSHD(ctx); err != nil {
		errs = append(errs, fmt.Errorf("failed to enable SSHD: %w", err))
	}
	if err := processMetaData(ctx, mnt); err != nil {
		errs = append(errs, fmt.Errorf("failed to process meta data: %w", err))
	}
	if err := processUserData(ctx, mnt); err != nil {
		errs = append(errs, fmt.Errorf("failed to process user data: %w", err))
	}
	if err := runBootScripts(ctx); err != nil {
		errs = append(errs, fmt.Errorf("failed to run boot scripts: %w", err))
	}
	return errors.Join(errs...)
}

func enableSSHD(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "/bin/launchctl", "load", "-w", "/System/Library/LaunchDaemons/ssh.plist")
	logrus.Infof("Executing command: %v", cmd.Args)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to execute command %v: %w (output=%#q)", cmd.Args, err, output)
	}
	return nil
}

func processMetaData(ctx context.Context, mnt string) error {
	metaDataPath := filepath.Join(mnt, "meta-data")
	metaDataB, err := os.ReadFile(metaDataPath)
	if err != nil {
		return fmt.Errorf("failed to read meta data file %#q: %w", metaDataPath, err)
	}
	var metaData cloudinittypes.MetaData
	if err = yaml.Unmarshal(metaDataB, &metaData); err != nil {
		return fmt.Errorf("failed to unmarshal meta data YAML: %w", err)
	}

	var errs []error
	logrus.Infof("Instance ID: %#q", metaData.InstanceID)
	if metaData.LocalHostname != "" {
		if err = setLocalHostname(ctx, metaData.LocalHostname); err != nil {
			errs = append(errs, fmt.Errorf("failed to set local hostname: %w", err))
		}
	}
	return errors.Join(errs...)
}

func setLocalHostname(ctx context.Context, hostname string) error {
	cmd := exec.CommandContext(ctx, "scutil", "--set", "LocalHostName", hostname)
	logrus.Infof("Executing command: %v", cmd.Args)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to execute command %v: %w (output=%#q)", cmd.Args, err, output)
	}
	return nil
}

func processUserData(ctx context.Context, mnt string) error {
	userDataPath := filepath.Join(mnt, "user-data")
	userDataB, err := os.ReadFile(userDataPath)
	if err != nil {
		return fmt.Errorf("failed to read user data file %#q: %w", userDataPath, err)
	}
	var userData cloudinittypes.UserData
	if err = yaml.Unmarshal(userDataB, &userData); err != nil {
		return fmt.Errorf("failed to unmarshal user data YAML: %w", err)
	}

	var errs []error
	if userData.Growpart != nil {
		logrus.Warn("growpart is not implemented")
	}
	if userData.PackageUpdate {
		logrus.Warn("package_update is not implemented")
	}
	if userData.PackageUpgrade {
		logrus.Warn("package_upgrade is not implemented")
	}
	if userData.PackageRebootIfRequired {
		logrus.Warn("package_reboot_if_required is not implemented")
	}
	for _, m := range userData.Mounts {
		if err = mountFSTabEntry(m); err != nil {
			errs = append(errs, fmt.Errorf("failed to mount fstab entry %v: %w", m, err))
		}
	}
	if userData.Timezone != "" {
		if err = setTimezone(ctx, userData.Timezone); err != nil {
			errs = append(errs, fmt.Errorf("failed to set timezone: %w", err))
		}
	}
	for _, u := range userData.Users {
		if err := createUser(ctx, &u, mnt); err != nil {
			errs = append(errs, fmt.Errorf("failed to create user %#q: %w", u.Name, err))
		}
	}
	for _, entry := range userData.WriteFiles {
		if err := writeFiles(ctx, entry); err != nil {
			errs = append(errs, fmt.Errorf("failed to write file for path %#q: %w", entry.Path, err))
		}
	}
	if userData.ManageResolvConf && userData.ResolvConf != nil {
		if err = setResolvConf(ctx, userData.ResolvConf); err != nil {
			errs = append(errs, fmt.Errorf("failed to apply DNS configuration: %w", err))
		}
	}
	if userData.CACerts != nil {
		logrus.Warn("ca_certs is not implemented")
	}
	if len(userData.BootCmd) > 0 {
		logrus.Warn("bootcmd is not implemented")
	}
	return errors.Join(errs...)
}

// mountFSTabEntry mounts a filesystem based on the given fstab entry.
// The format mimics Linux's convention.
// The entries are not written to /etc/fstab.
func mountFSTabEntry(m []string) error {
	if len(m) != 6 {
		return fmt.Errorf("invalid fstab entry: expected 6 fields, got %d: %v", len(m), m)
	}
	src, dst, fsType := m[0], m[1], m[2]
	switch fsType {
	case "virtiofs":
		return mountVirtiofs(src, dst)
	default:
		return fmt.Errorf("unsupported filesystem type %#q for fstab entry: %v", fsType, m)
	}
}

// mountVirtiofs symlinks `/Volumes/My Shared Files/<pseudoTag>` (automatically mounted by macOS) to dir.
// dir must not exist, or, must be a symlink to the expected source.
func mountVirtiofs(pseudoTag, dir string) error {
	if strings.Contains(pseudoTag, string(filepath.Separator)) {
		return fmt.Errorf("invalid pseudo tag for virtiofs: %#q", pseudoTag)
	}
	lnSrc := filepath.Join("/Volumes/My Shared Files", pseudoTag)

	// FIXME: verify that the filesystem of lnSrc is indeed read-only when user-data contains the "ro" option.
	// unix.Statfs() could be used, but unix.Statfs_t.Flags & unix.MNT_RDONLY seems always 0 for virtiofs.
	// `mount -v` does not show "ro" flag either.

	lnExisting, err := os.Readlink(dir)
	if err == nil {
		if lnExisting == lnSrc {
			return nil // already symlinked
		}
		return fmt.Errorf("unexpected symlink target for virtiofs mount source %#q: expected %#q, got %#q", lnSrc, lnSrc, lnExisting)
	}
	if !errors.Is(err, os.ErrNotExist) {
		logrus.WithError(err).Warnf("Failed to read existing symlink for virtiofs mount source %#q", lnSrc)
	}
	if err := os.Symlink(lnSrc, dir); err != nil {
		return fmt.Errorf("failed to create symlink from %#q to %#q: %w", lnSrc, dir, err)
	}
	return nil
}

func setTimezone(ctx context.Context, timezone string) error {
	cmd := exec.CommandContext(ctx, "systemsetup", "-settimezone", timezone)
	logrus.Infof("Executing command: %v", cmd.Args)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to execute command %v: %w (output=%#q)", cmd.Args, err, output)
	}
	return nil
}

func generatePassword() (string, error) {
	const pwLen = 16
	// Avoid special characters to minimize potential keyboard layout issue in GUI
	pw, err := password.Generate(pwLen, pwLen/4, 0, false, false)
	if err != nil {
		return "", fmt.Errorf("failed to generate password: %w", err)
	}
	return pw, nil
}

func populateHomeDir(ctx context.Context, uid int, homedir string) error {
	cmds := [][]string{
		{"ditto", "--noqtn", "/System/Library/User Template/English.lproj", homedir},
		{"chown", "-R", fmt.Sprintf("%d:staff", uid), homedir},
		{"chmod", "700", homedir},
	}
	for _, args := range cmds {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		logrus.Infof("Executing command: %v", cmd.Args)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to execute command %v: %w (output=%#q)", cmd.Args, err, output)
		}
	}
	return nil
}

// setupAssistantPlistTemplate is the built-in com.apple.SetupAssistant.plist.
// The Build/Version stamps are what macOS checks to decide whether setup is
// already complete; without them macOS resets MiniBuddyLaunchReason to 13 on
// first GUI login.
const setupAssistantPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
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
	<key>LastPreLoginTasksPerformedBuild</key><string>{{.Build}}</string>
	<key>LastPreLoginTasksPerformedVersion</key><string>{{.Version}}</string>
	<key>LastSeenAgeRangeSelectionProductVersion</key><string>{{.Version}}</string>
	<key>LastSeenBuddyBuildVersion</key><string>{{.Build}}</string>
	<key>LastSeenCloudProductVersion</key><string>{{.Version}}</string>
	<key>LastSeenDiagnosticsProductVersion</key><string>{{.Version}}</string>
	<key>MiniBuddyLaunchReason</key><integer>0</integer>
	<key>MiniBuddyShouldLaunchToResumeSetup</key><false/>
	<key>SkipExpressSettingsUpdating</key><true/>
	<key>SkipFirstLoginOptimization</key><true/>
</dict>
</plist>
`

var setupAssistantPlistTmpl = template.Must(template.New("setupAssistantPlist").Parse(setupAssistantPlistTemplate))

// suppressFirstLoginScreens writes the SetupAssistant and SoftwareUpdate
// preference plists before first login, so macOS treats them as the guest's
// initial state instead of resetting them at GUI login. A custom plist at
// mnt/setup-assistant.plist overrides the built-in template if present.
func suppressFirstLoginScreens(ctx context.Context, mnt string, uid int, homedir string) error {
	prefsDir := filepath.Join(homedir, "Library/Preferences")
	if err := os.MkdirAll(prefsDir, 0o700); err != nil {
		return fmt.Errorf("failed to create Preferences dir %#q: %w", prefsDir, err)
	}
	if err := os.Chown(prefsDir, uid, -1); err != nil {
		logrus.WithError(err).Warnf("Failed to chown Preferences dir %#q", prefsDir)
	}

	var setupAssistantPlist string
	if customPlistPath := filepath.Join(mnt, "setup-assistant.plist"); mnt != "" {
		if data, err := os.ReadFile(customPlistPath); err == nil && len(data) > 0 {
			setupAssistantPlist = string(data)
			logrus.Infof("Using custom SetupAssistant plist from %#q", customPlistPath)
		}
	}
	if setupAssistantPlist == "" {
		buildVersion := "unknown"
		if out, err := exec.CommandContext(ctx, "sw_vers", "-buildVersion").Output(); err == nil {
			buildVersion = strings.TrimSpace(string(out))
		} else {
			logrus.WithError(err).Warn("Failed to get build version from sw_vers")
		}
		productVersion := "unknown"
		if out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output(); err == nil {
			productVersion = strings.TrimSpace(string(out))
		} else {
			logrus.WithError(err).Warn("Failed to get product version from sw_vers")
		}

		var buf strings.Builder
		data := struct{ Build, Version string }{Build: buildVersion, Version: productVersion}
		if err := setupAssistantPlistTmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("failed to render SetupAssistant plist template: %w", err)
		}
		setupAssistantPlist = buf.String()
	}

	saPlist := filepath.Join(prefsDir, "com.apple.SetupAssistant.plist")
	if err := os.WriteFile(saPlist, []byte(setupAssistantPlist), 0o600); err != nil {
		return fmt.Errorf("failed to write SetupAssistant plist %#q: %w", saPlist, err)
	}
	if err := os.Chown(saPlist, uid, -1); err != nil {
		logrus.WithError(err).Warnf("Failed to chown SetupAssistant plist %#q", saPlist)
	}

	// defaults write (not os.WriteFile) so cfprefsd owns the domain; raw file
	// writes get overwritten when softwareupdated rewrites the plist on first boot.
	swPrefs := [][]string{
		{"AutomaticCheckEnabled", "-bool", "true"},
		{"AutomaticDownload", "-bool", "false"},
		{"AutomaticallyInstallMacOSUpdates", "-bool", "false"},
		{"ConfigDataInstall", "-bool", "true"},
		{"CriticalUpdateInstall", "-bool", "false"},
	}
	for _, pref := range swPrefs {
		args := append([]string{"write", "/Library/Preferences/com.apple.SoftwareUpdate"}, pref...)
		cmd := exec.CommandContext(ctx, "defaults", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			// Non-fatal: the dialog is annoying but does not block VM operation.
			logrus.WithError(err).Warnf("Failed to set SoftwareUpdate pref %v (output=%#q)", pref[0], output)
		}
	}

	logrus.Infof("Suppressed first-login setup screens for uid %d in %#q", uid, homedir)
	return nil
}

func createUser(ctx context.Context, u *cloudinittypes.User, mnt string) error {
	homedir := u.Homedir
	if homedir == "" {
		return fmt.Errorf("homedir is required for user %#q", u.Name)
	}
	// With VZMacGuestProvisioningOptions (macOS 27+), Setup Assistant creates the
	// user on the first boot, so adopt that account instead of creating one.
	if pw, err := os.ReadFile(filepath.Join(mnt, "guest-provisioning-password")); err == nil {
		return adoptProvisionedUser(ctx, u, mnt, string(pw))
	}
	if osutil.FileExists(homedir) {
		logrus.Debugf("homedir %#q already exists, skipping user creation for user %#q", homedir, u.Name)
		return nil
	}
	if _, _, err := lookupDSUser(ctx, u.Name); err == nil {
		// Created by Setup Assistant via guest provisioning on an earlier boot.
		logrus.Debugf("user %#q already exists in directory services, skipping user creation", u.Name)
		return nil
	}
	if u.UID == "" {
		return fmt.Errorf("uid is required for user %#q", u.Name)
	}
	uid, err := strconv.Atoi(u.UID)
	if err != nil {
		return fmt.Errorf("invalid uid %#q for user %#q: %w", u.UID, u.Name, err)
	}
	pw, err := generatePassword()
	if err != nil {
		return fmt.Errorf("failed to generate password for user %#q: %w", u.Name, err)
	}
	args := []string{
		"-addUser", u.Name,
		"-UID", u.UID,
		"-password", "-",
		"-home", homedir,
		"-admin",
	}
	if u.Gecos != "" {
		args = append(args, "-fullName", u.Gecos)
	}
	if u.Shell != "" {
		args = append(args, "-shell", u.Shell)
	}
	if u.LockPasswd != "" {
		logrus.Warn("lock_passwd field is not implemented")
	}
	cmd := exec.CommandContext(ctx, "/usr/sbin/sysadminctl", args...)
	cmd.Stdin = strings.NewReader(pw)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	logrus.Infof("Executing command: %v", cmd.Args)
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("failed to execute command %v: %w", cmd.Args, err)
	}

	// Look up the actual UID assigned by the system, as it may differ from
	// the requested UID (e.g., when macOS Setup Assistant already created
	// the user with a different UID).
	if sysUser, err := user.Lookup(u.Name); err != nil {
		logrus.WithError(err).Warnf("Failed to look up user %#q, using requested UID %d", u.Name, uid)
	} else if actualUID, err := strconv.Atoi(sysUser.Uid); err != nil {
		logrus.WithError(err).Warnf("Failed to parse actual UID %#q for user %#q, using requested UID %d", sysUser.Uid, u.Name, uid)
	} else if actualUID != uid {
		logrus.Warnf("Requested UID %d for user %#q, but the system assigned UID %d; using the actual UID", uid, u.Name, actualUID)
		uid = actualUID
	}

	// sysadminctl does not create the custom home directory
	if err = populateHomeDir(ctx, uid, homedir); err != nil {
		return fmt.Errorf("failed to populate home directory for user %#q: %w", u.Name, err)
	}

	// Presence of setup-assistant.plist on the cidata volume (not any cloud-config
	// key) is what signals this feature is enabled -- see GenerateISO9660.
	if _, statErr := os.Stat(filepath.Join(mnt, "setup-assistant.plist")); statErr == nil {
		// Write first-login preference plists now, as root, before any GUI session starts.
		// cfprefsd reads these fresh at first login so macOS does not reset them.
		if err := suppressFirstLoginScreens(ctx, mnt, uid, homedir); err != nil {
			// Non-fatal: SSH provisioning (configure.sh) can still handle it if needed.
			logrus.WithError(err).Warnf("Failed to suppress first-login screens for user %#q", u.Name)
		}
	}

	cmd = exec.CommandContext(ctx, "chmod", "700", homedir)
	logrus.Infof("Executing command: %v", cmd.Args)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to execute command %v: %w (output=%#q)", cmd.Args, err, output)
	}

	if err := installUserFiles(u, uid, homedir, pw); err != nil {
		return err
	}
	logrus.Infof("Created user %#q. The password is stored in %#q", u.Name, filepath.Join(homedir, "password"))
	return nil
}

// adoptProvisionedUser waits for Setup Assistant to create the user from
// VZMacGuestProvisioningOptions, then installs Lima's files into that account.
// The account's home directory is the one Setup Assistant chose, which differs
// from the Lima default (/Users/<user>.guest).
func adoptProvisionedUser(ctx context.Context, u *cloudinittypes.User, mnt, pw string) error {
	logrus.Infof("Waiting for Setup Assistant to create user %#q from guest provisioning", u.Name)
	var (
		uid     int
		homedir string
	)
	if err := waitFor(ctx, 10*time.Minute, func() (err error) {
		uid, homedir, err = lookupDSUser(ctx, u.Name)
		return err
	}); err != nil {
		return fmt.Errorf("user %#q was not created by Setup Assistant: %w", u.Name, err)
	}
	if homedir != u.Homedir {
		if err := moveProvisionedHome(ctx, u.Name, uid, homedir, u.Homedir); err != nil {
			// The boot scripts use user.home, so make it lead to the account's home.
			logrus.WithError(err).Warnf("Keeping home %#q created by Setup Assistant; linking %#q to it", homedir, u.Homedir)
			if err := os.Symlink(homedir, u.Homedir); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("failed to link %#q to %#q: %w", u.Homedir, homedir, err)
			}
		} else {
			homedir = u.Homedir
		}
	}
	if !osutil.FileExists(homedir) {
		if err := populateHomeDir(ctx, uid, homedir); err != nil {
			return fmt.Errorf("failed to populate home directory for user %#q: %w", u.Name, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(mnt, "setup-assistant.plist")); statErr == nil {
		if err := suppressFirstLoginScreens(ctx, mnt, uid, homedir); err != nil {
			logrus.WithError(err).Warnf("Failed to suppress first-login screens for user %#q", u.Name)
		}
	}
	if err := installUserFiles(u, uid, homedir, pw); err != nil {
		return err
	}
	logrus.Infof("Adopted user %#q created by Setup Assistant (uid %d, home %#q)", u.Name, uid, homedir)
	if osutil.FileExists(filepath.Join(mnt, "guest-provisioning-autologin")) {
		if err := enableAutoLogin(ctx, u.Name, pw); err != nil {
			logrus.WithError(err).Warnf("Failed to enable automatic login for user %#q", u.Name)
		}
	}
	return nil
}

// moveProvisionedHome moves the home directory Setup Assistant created (always
// /Users/<name>; VZMacGuestProvisioningOptions cannot choose it) to the Lima home.
// It only does so after Setup Assistant has finished and while nothing runs as
// the user, since a logged-in session keeps using the old path. The vz driver
// leaves automatic login off for Setup Assistant so the user stays logged out.
func moveProvisionedHome(ctx context.Context, name string, uid int, src, dst string) error {
	// Setup Assistant writes .AppleSetupDone when it finishes.
	if err := waitFor(ctx, 10*time.Minute, func() error {
		if !osutil.FileExists("/var/db/.AppleSetupDone") {
			return errors.New("setup assistant has not finished")
		}
		return nil
	}); err != nil {
		return err
	}
	// Setup Assistant leaves a background launchd session running for the new
	// user (cfprefsd, secd, lsd, trustd, distnoted, ...) although nobody has
	// logged in; those processes keep the old home open. End that session.
	userDomain := "user/" + strconv.Itoa(uid)
	cmd := exec.CommandContext(ctx, "launchctl", "bootout", userDomain)
	logrus.Infof("Executing command: %v", cmd.Args)
	if output, err := cmd.CombinedOutput(); err != nil {
		logrus.WithError(err).Warnf("Failed to end the launchd session of uid %d (output=%#q)", uid, output)
	}
	if err := waitFor(ctx, time.Minute, func() error {
		// pgrep exits 1 when no process matches.
		out, err := exec.CommandContext(ctx, "pgrep", "-l", "-U", strconv.Itoa(uid)).Output()
		if err == nil {
			return fmt.Errorf("processes are running as uid %d: %#q", uid, strings.TrimSpace(string(out)))
		}
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() == 1 {
			return nil
		}
		return err
	}); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%#q already exists", dst)
	}
	logrus.Infof("Moving home of user %#q from %#q to %#q", name, src, dst)
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	cmd = exec.CommandContext(ctx, "dscl", ".", "-create", "/Users/"+name, "NFSHomeDirectory", dst)
	if output, err := cmd.CombinedOutput(); err != nil {
		if rerr := os.Rename(dst, src); rerr != nil {
			logrus.WithError(rerr).Errorf("Failed to move %#q back to %#q", dst, src)
		}
		return fmt.Errorf("failed to execute command %v: %w (output=%#q)", cmd.Args, err, output)
	}
	if output, err := exec.CommandContext(ctx, "dscacheutil", "-flushcache").CombinedOutput(); err != nil {
		logrus.WithError(err).Warnf("Failed to flush the directory services cache (output=%#q)", output)
	}
	if _, got, err := lookupDSUser(ctx, name); err != nil || got != dst {
		return fmt.Errorf("directory services still reports home %#q for user %#q: %w", got, name, err)
	}
	return nil
}

// waitFor calls check every 2 seconds until it returns nil or timeout passes.
func waitFor(ctx context.Context, timeout time.Duration, check func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		err := check()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gave up after %v: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// kcpasswordKey is the fixed XOR key macOS loginwindow uses to obfuscate the
// automatic login password in /etc/kcpassword.
var kcpasswordKey = []byte{0x7D, 0x89, 0x52, 0x23, 0xD2, 0xBC, 0xDD, 0xEA, 0xA3, 0xB9, 0x1F}

// encodeKCPassword returns pw in /etc/kcpassword format: XORed with
// kcpasswordKey and zero-padded to the next multiple of 12 bytes, always
// adding at least one byte so loginwindow finds the terminator.
func encodeKCPassword(pw string) []byte {
	b := make([]byte, (len(pw)/12+1)*12)
	copy(b, pw)
	for i := range b {
		b[i] ^= kcpasswordKey[i%len(kcpasswordKey)]
	}
	return b
}

// enableAutoLogin turns on automatic login for the user, as
// VZMacGuestProvisioningOptions would have. loginwindow only reads it at boot,
// so it takes effect from the next boot; restarting loginwindow on macOS 27.2
// does not log the user in.
func enableAutoLogin(ctx context.Context, name, pw string) error {
	if err := os.WriteFile("/etc/kcpassword", encodeKCPassword(pw), 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "defaults", "write", "/Library/Preferences/com.apple.loginwindow", "autoLoginUser", "-string", name)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to execute command %v: %w (output=%#q)", cmd.Args, err, output)
	}
	logrus.Infof("Enabled automatic login for user %#q from the next boot", name)
	return nil
}

// lookupDSUser returns the UID and home directory of a local directory services user.
func lookupDSUser(ctx context.Context, name string) (uid int, homedir string, err error) {
	out, err := exec.CommandContext(ctx, "dscl", ".", "-read", "/Users/"+name, "UniqueID", "NFSHomeDirectory").Output()
	if err != nil {
		return 0, "", fmt.Errorf("user %#q not found in directory services: %w", name, err)
	}
	for line := range strings.Lines(string(out)) {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "UniqueID":
			uid, err = strconv.Atoi(strings.TrimSpace(v))
		case "NFSHomeDirectory":
			homedir = strings.TrimSpace(v)
		}
	}
	if err != nil || homedir == "" {
		return 0, "", fmt.Errorf("failed to parse directory services record for user %#q (output=%#q)", name, out)
	}
	return uid, homedir, nil
}

// installUserFiles writes the password file, SSH authorized keys and sudoers
// entry for the user.
func installUserFiles(u *cloudinittypes.User, uid int, homedir, pw string) error {
	pwPath := filepath.Join(homedir, "password")
	if err := os.WriteFile(pwPath, []byte(pw+"\n"), 0o400); err != nil {
		return fmt.Errorf("failed to write password file for user %#q: %w", u.Name, err)
	}

	dotSSHPath := filepath.Join(homedir, ".ssh")
	if err := os.MkdirAll(dotSSHPath, 0o700); err != nil {
		return fmt.Errorf("failed to create .ssh directory for user %#q: %w", u.Name, err)
	}
	authKeysPath := filepath.Join(dotSSHPath, "authorized_keys")
	authKeysContent := strings.Join(u.SSHAuthorizedKeys, "\n")
	if err := os.WriteFile(authKeysPath, []byte(authKeysContent), 0o600); err != nil {
		return fmt.Errorf("failed to write authorized_keys file for user %#q: %w", u.Name, err)
	}
	for _, f := range []string{pwPath, dotSSHPath, authKeysPath} {
		if err := os.Chown(f, uid, -1); err != nil {
			return fmt.Errorf("failed to chown %#q for user %#q: %w", f, u.Name, err)
		}
	}
	if u.Sudo != "" {
		if err := writeSudoers(u.Name, u.Sudo); err != nil {
			return fmt.Errorf("failed to write sudoers file for user %#q: %w", u.Name, err)
		}
	}
	return nil
}

// writeSudoers appends a sudoers entry for the given user.
// writeSudoers is expected be called only once on creating the user account.
func writeSudoers(userName, sudo string) error {
	if strings.Contains(sudo, "\n") {
		return errors.New("sudo field must not contain newline characters")
	}
	if err := os.MkdirAll("/etc/sudoers.d", 0o700); err != nil {
		return fmt.Errorf("failed to create /etc/sudoers.d directory: %w", err)
	}
	sudoersPath := "/etc/sudoers.d/90-cloud-init-users"
	f, err := os.OpenFile(sudoersPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o400)
	if err != nil {
		return fmt.Errorf("failed to open sudoers file %#q: %w", sudoersPath, err)
	}
	if _, err = fmt.Fprintf(f, "%s %s\n", userName, sudo); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to write to sudoers file %#q for user %#q: %w", sudoersPath, userName, err)
	}
	return f.Close()
}

func writeFiles(ctx context.Context, entry cloudinittypes.WriteFile) error {
	if entry.Path == "" {
		return errors.New("path is required for write_files entry")
	}
	perm := os.FileMode(0o644)
	if entry.Permissions != "" {
		p, err := strconv.ParseUint(entry.Permissions, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid permissions %#q for path %#q: %w", entry.Permissions, entry.Path, err)
		}
		perm = os.FileMode(p)
	}
	if err := os.MkdirAll(filepath.Dir(entry.Path), 0o755); err != nil {
		return fmt.Errorf("failed to create parent directory for path %#q: %w", entry.Path, err)
	}
	if err := os.WriteFile(entry.Path, []byte(entry.Content), perm); err != nil {
		return fmt.Errorf("failed to write file for path %#q: %w", entry.Path, err)
	}
	if entry.Owner != "" {
		cmd := exec.CommandContext(ctx, "chown", entry.Owner, entry.Path)
		logrus.Infof("Executing command: %v", cmd.Args)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to execute command %v: %w (output=%#q)", cmd.Args, err, output)
		}
	}
	return nil
}

func setResolvConf(ctx context.Context, resolvConf *cloudinittypes.ResolvConf) error {
	// FIXME: avoid hardcoding the primary network name
	const primaryNetwork = "Ethernet"
	cmd := exec.CommandContext(ctx, "networksetup", append([]string{"-setdnsservers", primaryNetwork}, resolvConf.Nameservers...)...)
	logrus.Infof("Executing command: %v", cmd.Args)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to execute command %v: %w (output=%#q)", cmd.Args, err, output)
	}
	return nil
}

func runBootScripts(ctx context.Context) error {
	dirs := []string{
		"/var/lib/cloud/scripts/per-boot",
	}
	var errs []error
	for _, dir := range dirs {
		dirEntries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("failed to read boot scripts directory %#q: %w", dir, err)
		}
		for _, entry := range dirEntries {
			if entry.IsDir() {
				continue
			}
			scriptPath := filepath.Join(dir, entry.Name())
			// entry.Type().Mode() does not seem to contain permission bits
			entryInfo, err := entry.Info()
			if err != nil {
				logrus.Warnf("Skipping boot script %#q due to stat error: %v", scriptPath, err)
				continue
			}
			if entryInfo.Mode().Perm()&0o111 == 0 {
				logrus.Warnf("Skipping non-executable boot script %#q (%v)", scriptPath, entryInfo.Mode().Perm())
				continue
			}
			cmd := exec.CommandContext(ctx, scriptPath)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			logrus.Infof("Executing command: %v", cmd.Args)
			if err := cmd.Run(); err != nil {
				errs = append(errs, fmt.Errorf("failed to execute command %v: %w", cmd.Args, err))
			}
		}
	}
	return errors.Join(errs...)
}
