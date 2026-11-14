// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package limayaml

import (
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/lima-vm/lima/v2/pkg/limatype"
)

func TestGuestProvisioningMovesHome(t *testing.T) {
	provisioning := limatype.OsOpts{limatype.DARWIN: limatype.DarwinOpts{
		GuestProvisioning: &limatype.DarwinGuestProvisioning{Password: "secret"},
	}}
	fill := func(y limatype.LimaYAML) limatype.LimaYAML {
		FillDefault(t.Context(), &y, &limatype.LimaYAML{}, &limatype.LimaYAML{}, filepath.Join(t.TempDir(), "lima.yaml"), false)
		return y
	}
	darwin := new(limatype.DARWIN)

	t.Run("the default home is moved", func(t *testing.T) {
		y := fill(limatype.LimaYAML{OS: darwin, OsOpts: provisioning})
		assert.Equal(t, *y.User.Home, "/Users/"+*y.User.Name+".guest")
		assert.Equal(t, GuestProvisioningHome(&y), "/Users/"+*y.User.Name)
		assert.Assert(t, GuestProvisioningMovesHome(&y))
	})

	t.Run("a home at Setup Assistant's path is kept", func(t *testing.T) {
		y := fill(limatype.LimaYAML{OS: darwin, OsOpts: provisioning, User: limatype.User{Name: new("custom"), Home: new("/Users/custom")}})
		assert.Assert(t, !GuestProvisioningMovesHome(&y))
	})
}
