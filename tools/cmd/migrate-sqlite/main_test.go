package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestRunAppliesMigrationsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	migration := []byte("CREATE TABLE example (value TEXT NOT NULL); INSERT INTO example (value) VALUES ('applied');")
	if err := os.WriteFile(filepath.Join(dir, "000001_example.up.sql"), migration, 0o600); err != nil {
		t.Fatal(err)
	}

	databasePath := filepath.Join(t.TempDir(), "test.db")
	args := []string{"-path", dir, "-database", "sqlite://" + databasePath, "up"}
	if err := run(args); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := run(args); err != nil {
		t.Fatalf("idempotent run: %v", err)
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()

	var value string
	if err := db.QueryRowContext(context.Background(), "SELECT value FROM example").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "applied" {
		t.Fatalf("value = %q, want applied", value)
	}
}

func TestRunRejectsUnsupportedCommand(t *testing.T) {
	t.Parallel()

	err := run([]string{"-path", t.TempDir(), "-database", "sqlite://ignored.db", "down"})
	if err == nil {
		t.Fatal("run() accepted unsupported command")
	}
}
