package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/core/ports"
)

func TestRunWritesPrivateAtomicJSONLExport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "moco.db")
	store, err := db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 2; index++ {
		if _, err := store.AppendAuditEvent(ctx, ports.AuditEvent{
			OccurredAt: time.Date(2026, 8, 29, 12, index, 0, 0, time.UTC), RequestID: "request-" + string(rune('0'+index)),
			Method: "GET", Route: "/api/v1", StatusCode: 200, Outcome: "success",
		}); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(directory, "audit.jsonl")
	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{"-database", databasePath, "-output", outputPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(outputPath) // #nosec G304 -- path is derived from this test's private TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(content), "\n"); got != 2 || !strings.Contains(string(content), `"sequence":1`) || !strings.Contains(string(content), `"sequence":2`) {
		t.Fatalf("JSONL output = %q", content)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "exported 2 audit events through sequence 2") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("output permissions = %o, want 600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".audit.jsonl.tmp-") {
			t.Fatalf("temporary export was left behind: %s", entry.Name())
		}
	}
}

func TestRunDoesNotOverwriteExistingExport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "moco.db")
	store, err := db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(directory, "audit.jsonl")
	if err := os.WriteFile(outputPath, []byte("sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{"-database", databasePath, "-output", outputPath}, &stdout, &stderr); err == nil {
		t.Fatal("existing output unexpectedly overwritten")
	}
	content, err := os.ReadFile(outputPath) // #nosec G304 -- path is derived from this test's private TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "sentinel\n" {
		t.Fatalf("existing output changed to %q", content)
	}
}

func TestRunStreamsJSONLToStdout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "moco.db")
	store, err := db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendAuditEvent(ctx, ports.AuditEvent{
		OccurredAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC), RequestID: "request-1",
		Method: "GET", Route: "/api/v1", StatusCode: 200, Outcome: "success",
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{"-database", databasePath, "-output", "-"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stdout.String(), "\n") || !strings.Contains(stdout.String(), `"requestId":"request-1"`) {
		t.Fatalf("stdout JSONL = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "exported 1 audit events") {
		t.Fatalf("stderr summary = %q", stderr.String())
	}
}
