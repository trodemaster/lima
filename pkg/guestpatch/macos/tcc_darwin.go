// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package macos

import (
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lima-vm/lima/v2/pkg/limatype"
)

// tccEntry is one row of the access table. insertTCCEntry writes every entry for a
// client given by its path (client_type 1) as allowed (auth_value 2), set by the
// system (auth_reason 4), with auth_version 1.
type tccEntry struct {
	service string
	client  string
	csreq   []byte // nil = no csreq
}

// csreq blobs are compiled code requirements, generated with:
//
//	csreq -r='identifier "com.apple.sshd-keygen-wrapper" and anchor apple' -b /dev/stdout | xxd -p
//
// The requirement matches `codesign -dr - <binary>` in the guest. These are
// identifier+AppleAnchor requirements — stable for Apple-signed system binaries.
// TestPresetCSReqs decodes them; tccd keeps but ignores an entry whose csreq doesn't match.
var csreqSSHDKeygen = mustDecodeHex(
	"FADE0C000000003C0000000100000006000000020000001D" +
		"636F6D2E6170706C652E737368642D6B657967656E2D7772617070657200000000000003")

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("invalid csreq hex literal: " + err.Error())
	}
	return b
}

// tccPresets maps preset name → one or more TCC entries to insert.
// Captured from macOS 15 after manually granting each permission.
var tccPresets = map[string][]tccEntry{
	"sshd-full-disk-access": {
		{
			service: "kTCCServiceSystemPolicyAllFiles",
			client:  "/usr/libexec/sshd-keygen-wrapper",
			csreq:   csreqSSHDKeygen,
		},
	},
	// lima-guestagent-full-disk-access: allows the lima guest agent to read any file.
	// The agent binary is on the cidata volume at /Volumes/cidata/lima-guestagent.
	// csreq is nil (NULL) because the binary is ad-hoc signed and changes with
	// every build. tccd accepts the entry and records the binary's cdhash on
	// first use.
	"lima-guestagent-full-disk-access": {
		{
			service: "kTCCServiceSystemPolicyAllFiles",
			client:  "/Volumes/cidata/lima-guestagent",
		},
	},
}

// patchTCC writes TCC permission entries into the system TCC database on
// the mounted guest disk. It creates the database and directory structure
// if they do not already exist, and returns the relative paths of any newly
// created filesystem entries (for subsequent ownership fixup via apfs.Chown).
func patchTCC(ctx context.Context, mnt string, perms []limatype.TCCPermission) ([]string, error) {
	if len(perms) == 0 {
		return nil, nil
	}

	if err := ValidateTCCPermissions(perms); err != nil {
		return nil, err
	}
	var entries []tccEntry
	for _, p := range perms {
		entries = append(entries, tccPresets[p.Preset]...)
	}

	// Build directory path and track what we create for later ownership fixup.
	tccRelDir := filepath.Join("Library", "Application Support", "com.apple.TCC")
	tccAbsDir := filepath.Join(mnt, tccRelDir)
	tccDB := filepath.Join(tccAbsDir, "TCC.db")

	created, err := mkdirAllTracked(mnt, tccRelDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create TCC directory: %w", err)
	}

	// Initialize the database schema (full schema matching macOS 15).
	schema := `
CREATE TABLE IF NOT EXISTS admin (
    key   TEXT PRIMARY KEY NOT NULL,
    value INTEGER NOT NULL
);
INSERT OR REPLACE INTO admin VALUES ('version', 30);
CREATE TABLE IF NOT EXISTS policies (
    id        INTEGER NOT NULL PRIMARY KEY,
    bundle_id TEXT    NOT NULL,
    uuid      TEXT    NOT NULL,
    display   TEXT    NOT NULL,
    UNIQUE (bundle_id, uuid)
);
CREATE TABLE IF NOT EXISTS active_policy (
    client      TEXT    NOT NULL,
    client_type INTEGER NOT NULL,
    policy_id   INTEGER NOT NULL,
    PRIMARY KEY (client, client_type),
    FOREIGN KEY (policy_id) REFERENCES policies(id) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX IF NOT EXISTS active_policy_id ON active_policy(policy_id);
CREATE TABLE IF NOT EXISTS access (
    service                            TEXT    NOT NULL,
    client                             TEXT    NOT NULL,
    client_type                        INTEGER NOT NULL,
    auth_value                         INTEGER NOT NULL,
    auth_reason                        INTEGER NOT NULL,
    auth_version                       INTEGER NOT NULL,
    csreq                              BLOB,
    policy_id                          INTEGER,
    indirect_object_identifier_type    INTEGER,
    indirect_object_identifier         TEXT    NOT NULL DEFAULT 'UNUSED',
    indirect_object_code_identity      BLOB,
    flags                              INTEGER,
    last_modified                      INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    pid                                INTEGER,
    pid_version                        INTEGER,
    boot_uuid                          TEXT    NOT NULL DEFAULT 'UNUSED',
    last_reminded                      INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY (service, client, client_type, indirect_object_identifier),
    FOREIGN KEY (policy_id) REFERENCES policies(id) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE IF NOT EXISTS access_overrides (service TEXT NOT NULL PRIMARY KEY);
CREATE TABLE IF NOT EXISTS expired (
    service       TEXT    NOT NULL,
    client        TEXT    NOT NULL,
    client_type   INTEGER NOT NULL,
    csreq         BLOB,
    last_modified INTEGER NOT NULL,
    expired_at    INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY (service, client, client_type)
);`
	if err := runSQLite(ctx, tccDB, schema); err != nil {
		return nil, fmt.Errorf("failed to initialize TCC database schema: %w", err)
	}

	for _, e := range entries {
		if err := insertTCCEntry(ctx, tccDB, e); err != nil {
			return nil, err
		}
	}

	created = append(created, filepath.Join(tccRelDir, "TCC.db"))
	return created, nil
}

// insertTCCEntry inserts or replaces one row in the TCC access table.
func insertTCCEntry(ctx context.Context, dbPath string, e tccEntry) error {
	// Build csreq literal: X'hex' or NULL.
	csreqLit := "NULL"
	if len(e.csreq) > 0 {
		csreqLit = "X'" + hex.EncodeToString(e.csreq) + "'"
	}

	sql := fmt.Sprintf(
		`INSERT OR REPLACE INTO access
		 (service, client, client_type, auth_value, auth_reason, auth_version,
		  csreq, indirect_object_identifier, boot_uuid, last_reminded)
		 VALUES (%s, %s, 1, 2, 4, 1,
		         %s, 'UNUSED', 'UNUSED', CAST(strftime('%%s','now') AS INTEGER));`,
		sqlQuote(e.service), sqlQuote(e.client), csreqLit,
	)
	return runSQLite(ctx, dbPath, sql)
}

// mkdirAllTracked creates each component of relPath under base one at a time,
// returning the relative paths of directories that did not already exist.
func mkdirAllTracked(base, relPath string) ([]string, error) {
	parts := strings.Split(relPath, string(filepath.Separator))
	var created []string
	current := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if current == "" {
			current = part
		} else {
			current = filepath.Join(current, part)
		}
		abs := filepath.Join(base, current)
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			if err := os.Mkdir(abs, 0o755); err != nil {
				return nil, err
			}
			created = append(created, current)
		}
	}
	return created, nil
}

// runSQLite executes sql against dbPath using the system sqlite3 binary.
func runSQLite(ctx context.Context, dbPath, sql string) error {
	cmd := exec.CommandContext(ctx, "sqlite3", dbPath, sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sqlite3: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sqlQuote wraps s in single quotes, escaping embedded single quotes.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ValidateTCCPermissions reports a preset name that has no built-in entries.
func ValidateTCCPermissions(perms []limatype.TCCPermission) error {
	for _, p := range perms {
		if _, ok := tccPresets[p.Preset]; !ok {
			return fmt.Errorf("unknown TCC preset %q; available: %s",
				p.Preset, strings.Join(slices.Sorted(maps.Keys(tccPresets)), ", "))
		}
	}
	return nil
}
