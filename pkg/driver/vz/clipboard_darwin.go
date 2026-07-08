//go:build darwin && !no_vz

// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package vz

import (
	"github.com/Code-Hex/vz/v3"
	"github.com/sirupsen/logrus"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limayaml"
)

// attachClipboard configures the VZ SPICE agent port for host-guest clipboard
// sharing. This only sets up the host side of the channel; a SPICE
// vdagent-compatible agent must be running in the guest for clipboard sharing
// to actually work. Lima does not ship or install that guest agent.
func attachClipboard(inst *limatype.Instance, vmConfig *vz.VirtualMachineConfiguration) error {
	if *inst.Config.OS != limatype.DARWIN {
		return nil
	}

	var darwinOpts limatype.DarwinOpts
	if err := limayaml.Convert(inst.Config.OsOpts[limatype.DARWIN], &darwinOpts, "osOpts.darwin"); err != nil {
		return err
	}
	if darwinOpts.Clipboard == nil || !*darwinOpts.Clipboard {
		return nil
	}

	portName, err := vz.SpiceAgentPortAttachmentName()
	if err != nil {
		logrus.Warnf("osOpts.darwin.clipboard is enabled, but the SPICE agent port name could not be determined: %v", err)
		return nil
	}
	spiceAgent, err := vz.NewSpiceAgentPortAttachment()
	if err != nil {
		logrus.Warnf("osOpts.darwin.clipboard is enabled, but the SPICE agent port attachment could not be created: %v", err)
		return nil
	}
	spiceAgent.SetSharesClipboard(true)

	portConfig, err := vz.NewVirtioConsolePortConfiguration(
		vz.WithVirtioConsolePortConfigurationName(portName),
		vz.WithVirtioConsolePortConfigurationAttachment(spiceAgent),
	)
	if err != nil {
		return err
	}

	consoleDevice, err := vz.NewVirtioConsoleDeviceConfiguration()
	if err != nil {
		return err
	}
	consoleDevice.SetVirtioConsolePortConfiguration(0, portConfig)

	vmConfig.SetConsoleDevicesVirtualMachineConfiguration([]vz.ConsoleDeviceConfiguration{
		consoleDevice,
	})
	return nil
}
