// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package macos

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lima-vm/lima/v2/pkg/limatype"
)

type tccEntry struct {
	service     string
	client      string
	clientType  int    // 0=bundle, 1=path
	authValue   int    // 0=deny, 2=allow
	authVersion int    // usually 1; kTCCServicePostEvent uses 2
	csreq       []byte // nil = no csreq
}

// csreq blobs captured from a live macOS 15 VM via:
//
//	sqlite3 "/Library/Application Support/com.apple.TCC/TCC.db" "SELECT service,client,hex(csreq) FROM access;"
//
// These are identifier+AppleAnchor requirements — stable for Apple-signed system binaries.
var (
	csreqTerminal = mustDecodeHex(
		"FADE0C000000003000000001000000060000000200000012" +
			"636F6D2E6170706C652E5465726D696E616C000000000003")
	csreqSSHDKeygen = mustDecodeHex(
		"FADE0C000000003C0000000100000006000000020000001D" +
			"636F6D2E6170706C652E737368642D6B657967656E2D777261707065720000000000000003")
)

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
			service:     "kTCCServiceSystemPolicyAllFiles",
			client:      "/usr/libexec/sshd-keygen-wrapper",
			clientType:  1,
			authValue:   2,
			authVersion: 1,
			csreq:       csreqSSHDKeygen,
		},
	},
	"terminal-accessibility": {
		{
			service:     "kTCCServiceAccessibility",
			client:      "com.apple.Terminal",
			clientType:  0,
			authValue:   2,
			authVersion: 1,
			csreq:       csreqTerminal,
		},
		{
			service:     "kTCCServicePostEvent",
			client:      "com.apple.Terminal",
			clientType:  0,
			authValue:   2,
			authVersion: 2,
			csreq:       csreqTerminal,
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

	var entries []tccEntry
	for _, p := range perms {
		if p.Preset != "" {
			preset, ok := tccPresets[p.Preset]
			if !ok {
				return nil, fmt.Errorf("unknown TCC preset %q; available: %s",
					p.Preset, availablePresets())
			}
			entries = append(entries, preset...)
			continue
		}
		e := tccEntry{service: p.Service, client: p.Client}
		switch p.ClientType {
		case "bundle":
			e.clientType = 0
		case "path":
			e.clientType = 1
		default:
			return nil, fmt.Errorf("tccPermission clientType %q: expected \"bundle\" or \"path\"", p.ClientType)
		}
		switch p.AuthValue {
		case "allow", "":
			e.authValue = 2
		case "deny":
			e.authValue = 0
		default:
			return nil, fmt.Errorf("tccPermission authValue %q: expected \"allow\" or \"deny\"", p.AuthValue)
		}
		e.authVersion = 1
		entries = append(entries, e)
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
		 VALUES (%s, %s, %d, %d, 4, %d,
		         %s, 'UNUSED', 'UNUSED', CAST(strftime('%%s','now') AS INTEGER));`,
		sqlQuote(e.service), sqlQuote(e.client),
		e.clientType, e.authValue, e.authVersion,
		csreqLit,
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

func availablePresets() string {
	names := make([]string, 0, len(tccPresets))
	for k := range tccPresets {
		names = append(names, k)
	}
	return strings.Join(names, ", ")
}
