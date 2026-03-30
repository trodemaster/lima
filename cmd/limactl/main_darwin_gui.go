//go:build darwin

// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package main

import "runtime"

func init() {
	// Pin the main goroutine to the main OS thread before main() runs.
	// macOS requires all Cocoa/AppKit GUI operations (NSApplication, NSWindow,
	// VZVirtualMachineView, etc.) to execute on the process's main thread
	// (thread 0). Without this, Go's scheduler may migrate the main goroutine
	// to a worker thread before the hostagent's LockOSThread() call, causing
	// SIGTRAP crashes when the VZ GUI tries to connect to WindowServer.
	runtime.LockOSThread()
}
