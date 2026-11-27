// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package macos

import (
	"encoding/binary"
	"testing"

	"gotest.tools/v3/assert"
)

// decodeIdentifierAnchorApple decodes a code requirement blob of the form
// `identifier "<id>" and anchor apple` and returns the identifier.
// It fails the test on any other layout, so a mistyped hex literal
// (wrong length, wrong padding, wrong opcode) is caught here instead of
// being silently ignored by tccd in the guest.
func decodeIdentifierAnchorApple(t *testing.T, blob []byte) string {
	t.Helper()
	const (
		csMagicRequirement = 0xfade0c00
		exprForm           = 1
		opIdent            = 2
		opAppleAnchor      = 3
		opAnd              = 6
	)
	u32 := func(off int) uint32 {
		assert.Assert(t, off+4 <= len(blob), "blob truncated at offset %d", off)
		return binary.BigEndian.Uint32(blob[off:])
	}
	assert.Equal(t, u32(0), uint32(csMagicRequirement), "magic")
	assert.Equal(t, int(u32(4)), len(blob), "length field must match blob length")
	assert.Equal(t, u32(8), uint32(exprForm), "kind")
	assert.Equal(t, u32(12), uint32(opAnd), "first op")
	assert.Equal(t, u32(16), uint32(opIdent), "second op")
	n := int(u32(20))
	id := string(blob[24 : 24+n])
	off := 24 + (n+3)/4*4 // identifier data is padded to 4 bytes
	assert.Equal(t, u32(off), uint32(opAppleAnchor), "third op")
	assert.Equal(t, off+4, len(blob), "trailing bytes after anchor apple")
	return id
}

func TestPresetCSReqs(t *testing.T) {
	// Expected values: `codesign -dr - <binary>` in the guest.
	assert.Equal(t, decodeIdentifierAnchorApple(t, csreqSSHDKeygen), "com.apple.sshd-keygen-wrapper")
}
