// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package cidata

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
)

// TestCheckWindowsVersionFixtures exercises checkWindowsVersion and
// selectWindowsImage against tiny, structurally valid ISO fixtures built by
// pkg/udfutil/testdata/gen, all of which embed WIM/ESD XML metadata
// captured byte-for-byte from real Windows installer ISOs (see that
// generator's package doc comment for provenance).
func TestCheckWindowsVersionFixtures(t *testing.T) {
	testCases := []struct {
		fixture          string
		edition          string
		installationType string
		wantServer       bool
		wantIndex        int
		wantErrContains  string
	}{
		// 2 editions (Enterprise, Professional): the ISO that started this
		// whole investigation. No edition set -> defaults to Professional.
		{fixture: "windows11-client-2edition.iso", edition: "", wantServer: false, wantIndex: 2},
		{fixture: "windows11-client-2edition.iso", edition: "Enterprise", wantServer: false, wantIndex: 1},
		{fixture: "windows11-client-2edition.iso", edition: "professional", wantServer: false, wantIndex: 2}, // case-insensitive
		{fixture: "windows11-client-2edition.iso", edition: "Home", wantErrContains: "no image with edition"},

		// 8 editions, Professional already first: default and an
		// out-of-order case-insensitive match.
		{fixture: "windows11-client-8edition.iso", edition: "", wantServer: false, wantIndex: 1},
		{fixture: "windows11-client-8edition.iso", edition: "ioTEnterprise", wantServer: false, wantIndex: 7},

		// 9 editions, install.esd instead of install.wim: exercises the
		// install.wim -> install.esd fallback path end to end.
		{fixture: "windows11-client-9edition-esd.iso", edition: "", wantServer: false, wantIndex: 1},

		// Single edition, which happens to already be Professional.
		{fixture: "windows10-client-1edition.iso", edition: "", wantServer: false, wantIndex: 1},

		// Windows Server 2025 Evaluation: the exact ISO templates/windows-2025.yaml
		// downloads (https://aka.ms/WinServ2025iso-enus). 4 images: Standard
		// and Datacenter, each as Core (index 1, 3) and Desktop Experience
		// (index 2, 4).
		//
		// No selection criteria at all: no Professional edition exists, so
		// the default falls back to the first image -- index 1 -- matching
		// what the old hardcoded `if IsWindowsServer: 1` resolved to on
		// this exact ISO.
		{fixture: "windows-server-2025-eval.iso", edition: "", installationType: "", wantServer: true, wantIndex: 1},

		// EDITIONID is NOT unique per image here: both Standard images
		// share "ServerStandardEval" (Core vs. Desktop Experience is only
		// distinguished by INSTALLATIONTYPE/DISPLAYNAME), and likewise for
		// the two Datacenter images. This is real observed data, not a
		// simplification. Edition alone, matching more than one image,
		// defaults to the non-Core (Desktop Experience) variant -- index 2,
		// not the first match (index 1) -- since that's the more broadly
		// usable default for someone who didn't ask for Core specifically.
		// Adding installationType disambiguates explicitly either way.
		{fixture: "windows-server-2025-eval.iso", edition: "ServerStandardEval", installationType: "", wantServer: true, wantIndex: 2},
		{fixture: "windows-server-2025-eval.iso", edition: "ServerStandardEval", installationType: "Server Core", wantServer: true, wantIndex: 1},
		{fixture: "windows-server-2025-eval.iso", edition: "ServerStandardEval", installationType: "server", wantServer: true, wantIndex: 2}, // case-insensitive
		{fixture: "windows-server-2025-eval.iso", edition: "serverdatacentereval", installationType: "", wantServer: true, wantIndex: 4},     // case-insensitive edition, defaults to non-Core (index 4)
		{fixture: "windows-server-2025-eval.iso", edition: "ServerDatacenterEval", installationType: "Server Core", wantServer: true, wantIndex: 3},
		{fixture: "windows-server-2025-eval.iso", edition: "Home", wantErrContains: "no image with edition"},
		{fixture: "windows-server-2025-eval.iso", edition: "ServerStandardEval", installationType: "Client", wantErrContains: "no image with edition `ServerStandardEval` and installation type `Client` found"},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s/edition=%s/installationType=%s", tc.fixture, tc.edition, tc.installationType), func(t *testing.T) {
			instDir := t.TempDir()
			src, err := filepath.Abs(filepath.Join("..", "udfutil", "testdata", tc.fixture))
			assert.NilError(t, err)
			assert.NilError(t, os.Symlink(src, filepath.Join(instDir, filenames.ISO)))

			args := &TemplateArgs{}
			err = args.checkWindowsVersion(instDir, tc.edition, tc.installationType)
			if tc.wantErrContains != "" {
				assert.ErrorContains(t, err, tc.wantErrContains)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, args.IsWindowsServer, tc.wantServer)
			assert.Equal(t, args.ImageIndex, tc.wantIndex)
		})
	}
}
