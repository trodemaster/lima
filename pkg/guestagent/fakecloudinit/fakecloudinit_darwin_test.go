// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package fakecloudinit

import (
	"encoding/hex"
	"testing"

	"gotest.tools/v3/assert"
)

func TestEncodeKCPassword(t *testing.T) {
	cases := []struct {
		pw   string
		want string // hex
	}{
		// Shorter than 12 bytes: padded to 12.
		{"password", "0de82150a5d3af8ea3b91f7d"},
		// Exactly 12 bytes: a full block of padding is added for the terminator.
		{"abcdefghijkl", "1ceb3147b7daba82cad37411895223d2bcddeaa3b91f7d89"},
	}
	for _, tc := range cases {
		t.Run(tc.pw, func(t *testing.T) {
			assert.Equal(t, hex.EncodeToString(encodeKCPassword(tc.pw)), tc.want)
		})
	}
}
