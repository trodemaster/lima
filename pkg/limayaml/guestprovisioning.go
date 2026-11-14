// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package limayaml

import "github.com/lima-vm/lima/v2/pkg/limatype"

// GuestProvisioningHome returns the home directory that Setup Assistant gives the
// account it creates from osOpts.Darwin.guestProvisioning. macOS has no option to
// choose it, so the guest moves it to `user.home` afterwards when the two differ.
func GuestProvisioningHome(y *limatype.LimaYAML) string {
	return "/Users/" + *y.User.Name
}

// GuestProvisioningMovesHome reports whether the guest has to move the provisioned
// account's home to `user.home`. Lima's default home for macOS guests,
// /Users/<user>.guest, always differs from Setup Assistant's /Users/<user>.
func GuestProvisioningMovesHome(y *limatype.LimaYAML) bool {
	return *y.User.Home != GuestProvisioningHome(y)
}
