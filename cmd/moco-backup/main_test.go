package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/araihu/moco/internal/adapters/backup"
	"github.com/araihu/moco/internal/adapters/db"
)

func TestCommandCreatesVerifiesAndRestores(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	source := filepath.Join(directory, "source.db")
	store, err := db.Open(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureEncryptionKeyState(t.Context(), "root-v1", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	keyring := filepath.Join(directory, "keys.json")
	keyBytes := bytes.Repeat([]byte{0x6b}, 32)
	encoded := base64.StdEncoding.EncodeToString(keyBytes)
	payload, err := json.Marshal(map[string]any{"activeKeyId": "root-v1", "keys": map[string]string{"root-v1": encoded}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyring, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(directory, "backup.tar")
	restored := filepath.Join(directory, "restored.db")
	var first backup.Manifest
	for i, args := range [][]string{
		{"create", "--database", source, "--output", archive},
		{"verify", "--backup", archive},
		{"verify", "--backup", archive, "--keyring", keyring},
		{"restore", "--backup", archive, "--database", restored, "--keyring", keyring},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(t.Context(), args, &stdout, &stderr); err != nil {
			t.Fatalf("%s: %v", args[0], err)
		}
		var manifest backup.Manifest
		if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = manifest
		}
		if manifest.DatabaseSHA256 != first.DatabaseSHA256 || manifest.State.ActiveRootKeyID != "root-v1" || manifest.State.EncryptionKeyEpoch != 1 {
			t.Fatalf("unexpected command result: %+v", manifest)
		}
		if stderr.Len() != 0 || strings.Contains(stdout.String(), encoded) {
			t.Fatal("unexpected diagnostics or leaked key")
		}
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	recovered, err := db.OpenReadOnly(t.Context(), restored)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recovered.Close() }()
	if _, err := recovered.InspectSnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCommandRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil, {"unknown"}, {"create"}, {"create", "--output", "-"},
		{"create", "--output", "unused.tar", "--timeout", "0"},
		{"create", "--output", "unused.tar", "unexpected"},
		{"verify"}, {"restore", "--backup", "missing.tar", "--database", "unused.db"},
		{"restore", "--backup", "missing.tar", "--keyring", "missing.json"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(t.Context(), args, &stdout, &stderr); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
		if stdout.Len() != 0 {
			t.Fatal("failed command printed a success summary")
		}
	}
}

func TestKeyringRejectsPublicAndMalformedFiles(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, payload string
		mode          os.FileMode
	}{
		{"public", `{"activeKeyId":"v1","keys":{"v1":"a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s="}}`, 0o644},
		{"invalid-json", `{`, 0o600},
		{"oversized", strings.Repeat(" ", (64<<10)+1), 0o600},
		{"missing-active-key", `{"activeKeyId":"v2","keys":{"v1":"a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s="}}`, 0o600},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keys.json")
			if err := os.WriteFile(path, []byte(test.payload), test.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, test.mode); err != nil { // #nosec G302 -- exercise rejection of public keyrings independently of umask.
				t.Fatal(err)
			}
			if _, err := readKeyring(path); err == nil {
				t.Fatal("invalid keyring accepted")
			}
		})
	}
}
