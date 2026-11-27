//go:build darwin && !no_vz

// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package vz

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/limayaml"
)

func TestWaitForStopped(t *testing.T) {
	t.Run("returns nil after the VM has stopped", func(t *testing.T) {
		l := &LimaVzDriver{machine: &virtualMachineWrapper{}}
		go func() {
			l.machine.mu.Lock()
			l.machine.stopped = true
			l.machine.mu.Unlock()
		}()
		assert.NilError(t, l.waitForStopped(10*time.Second))
	})

	t.Run("returns an error when the VM is still running", func(t *testing.T) {
		l := &LimaVzDriver{machine: &virtualMachineWrapper{}}
		assert.ErrorContains(t, l.waitForStopped(600*time.Millisecond), "timeout")
	})
}

func TestStartSignalsFailReleasesSSHWait(t *testing.T) {
	signals := newStartSignals()
	l := &LimaVzDriver{waitSSHLocalPortAccessible: signals.sshPortAccessible}
	driverErr := errors.New("driver failed")
	go signals.fail(driverErr)

	setupDone := make(chan error, 1)
	go func() { setupDone <- l.AdditionalSetupForSSH(t.Context()) }()
	select {
	case err := <-setupDone:
		assert.NilError(t, err)
	case <-time.After(10 * time.Second):
		assert.Assert(t, false, "AdditionalSetupForSSH still waits after the driver failed")
	}
	assert.Equal(t, <-signals.errCh, driverErr)
}

func TestRemovePIDFile(t *testing.T) {
	dir := t.TempDir()
	l := &LimaVzDriver{
		Instance: &limatype.Instance{
			Dir:    dir,
			Config: &limatype.LimaYAML{VMType: new(limatype.VZ)},
		},
	}
	pidFile := filepath.Join(dir, filenames.PIDFile(limatype.VZ))
	assert.NilError(t, os.WriteFile(pidFile, []byte("42\n"), 0o644))

	l.removePIDFile()
	_, err := os.Stat(pidFile)
	assert.Assert(t, errors.Is(err, os.ErrNotExist))

	// Removing a PID file that does not exist is not an error.
	l.removePIDFile()
}

func TestGVisorNetstackOptsHostLoopback(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		inst := &limatype.Instance{Dir: t.TempDir(), Config: &limatype.LimaYAML{NetworkOpts: limatype.NetworkOpts{HostLoopback: new(enabled)}}}
		assert.Equal(t, gvisorNetstackOpts(inst, "", "").DisableHostLoopback, !enabled)
	}
}

func TestValidateRejectsHostLoopbackFalseWithUserV2(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())
	cfg := &limatype.LimaYAML{
		Firmware:    limatype.Firmware{LegacyBIOS: new(false)},
		NetworkOpts: limatype.NetworkOpts{HostLoopback: new(false)},
		Networks:    []limatype.Network{{Lima: "user-v2"}},
	}
	l := &LimaVzDriver{Instance: &limatype.Instance{Config: cfg}}
	assert.ErrorContains(t, l.Validate(t.Context()), "not supported with a `user-v2` network")
}

func TestDarwinTCCPermissions(t *testing.T) {
	const osOpts = `
osOpts:
  Darwin:
    tccPermissions:
    - preset: sshd-full-disk-access
    - preset: lima-guestagent-full-disk-access
`
	load := func(t *testing.T, yaml string) *limatype.LimaYAML {
		cfg, err := limayaml.Load(t.Context(), []byte(yaml), filepath.Join(t.TempDir(), "lima.yaml"))
		assert.NilError(t, err)
		return cfg
	}

	t.Run("Darwin guest", func(t *testing.T) {
		perms, err := darwinTCCPermissions(load(t, "vmType: vz\nos: Darwin\n"+osOpts))
		assert.NilError(t, err)
		assert.DeepEqual(t, perms, []limatype.TCCPermission{
			{Preset: "sshd-full-disk-access"},
			{Preset: "lima-guestagent-full-disk-access"},
		})
	})
	t.Run("Linux guest", func(t *testing.T) {
		perms, err := darwinTCCPermissions(load(t, "vmType: vz\nos: Linux\n"+osOpts))
		assert.NilError(t, err)
		assert.Assert(t, perms == nil)
	})
	t.Run("unknown preset", func(t *testing.T) {
		cfg := load(t, "vmType: vz\nos: Darwin\nosOpts:\n  Darwin:\n    tccPermissions:\n    - preset: no-such-preset\n")
		_, err := darwinTCCPermissions(cfg)
		assert.ErrorContains(t, err, `unknown TCC preset "no-such-preset"`)
		// Configure, which deletion uses, must still succeed.
		_, err = New().Configure(t.Context(), &limatype.Instance{Config: cfg, Dir: t.TempDir()})
		assert.NilError(t, err)
	})
}

func TestGuestProvisioningSkipReason(t *testing.T) {
	cases := []struct {
		name         string
		hostMajor    int64
		guestVersion string
		wantErr      string // substring; "" means provisioning runs
	}{
		{"27 host, 27 guest", 27, "27.0.0", ""},
		{"28 host, 27 guest", 28, "27.1.0", ""},
		{"27 host, 26 guest", 27, "26.6.0", "needs macOS 27 or later"},
		{"26 host, 26 guest", 26, "26.6.0", "needs macOS 27 or later"},
		{"26 host, 27 guest", 26, "27.0.0", "host must be macOS 27 or later"},
		{"unknown guest", 27, "", "unknown"},
		{"unparsable guest", 27, "x.y", "cannot parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guestProvisioningSkipReason(tc.hostMajor, tc.guestVersion)
			if tc.wantErr == "" {
				assert.NilError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.wantErr)
			}
		})
	}
	assert.Assert(t, errors.Is(guestProvisioningSkipReason(26, "27.0.0"), errGuestProvisioningNeedsNewerHost))
}
