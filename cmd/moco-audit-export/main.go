// Command moco-audit-export writes a finite, read-only audit snapshot as JSONL.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/core/services"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "moco-audit-export:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("moco-audit-export", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("database", environmentOrDefault("MOCO_DB_PATH", "./moco.db"), "SQLite database path")
	outputPath := flags.String("output", "", "JSONL output path, or - for stdout")
	afterSequence := flags.Int64("after-sequence", 0, "exclusive audit sequence checkpoint")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*databasePath) == "" {
		return errors.New("database path is required")
	}
	if strings.TrimSpace(*outputPath) == "" {
		return errors.New("output path is required")
	}
	if *afterSequence < 0 {
		return errors.New("after-sequence must not be negative")
	}

	store, err := db.OpenReadOnly(ctx, *databasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = store.Close() }()
	exporter, err := services.NewAuditExportService(store)
	if err != nil {
		return fmt.Errorf("initialize exporter: %w", err)
	}

	writer := stdout
	commit := func() error { return nil }
	abort := func() {}
	if *outputPath != "-" {
		var output *atomicOutput
		output, err = newAtomicOutput(*outputPath)
		if err != nil {
			return err
		}
		writer, commit, abort = output.file, output.commit, output.abort
	}
	committed := false
	defer func() {
		if !committed {
			abort()
		}
	}()
	result, err := exporter.Export(ctx, writer, services.AuditExportRequest{AfterSequence: *afterSequence})
	if err != nil {
		return fmt.Errorf("export audit events: %w", err)
	}
	if err := commit(); err != nil {
		return fmt.Errorf("commit audit export: %w", err)
	}
	committed = true
	if _, err := fmt.Fprintf(stderr, "exported %d audit events through sequence %d (snapshot upper %d)\n", result.Exported, result.LastSequence, result.UpperSequence); err != nil {
		return fmt.Errorf("write export summary: %w", err)
	}
	return nil
}

type atomicOutput struct {
	file      *os.File
	temporary string
	target    string
	directory string
}

func newAtomicOutput(path string) (*atomicOutput, error) {
	path = filepath.Clean(path)
	if path == "." {
		return nil, errors.New("output path must name a file")
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("output path already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect output path: %w", err)
	}
	directory := filepath.Dir(path)
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect output directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("output parent is not a directory: %s", directory)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary export: %w", err)
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return nil, fmt.Errorf("restrict temporary export permissions: %w", err)
	}
	return &atomicOutput{file: temporary, temporary: temporary.Name(), target: path, directory: directory}, nil
}

func (o *atomicOutput) commit() error {
	if err := o.file.Sync(); err != nil {
		return fmt.Errorf("sync temporary export: %w", err)
	}
	if err := o.file.Close(); err != nil {
		return fmt.Errorf("close temporary export: %w", err)
	}
	// A hard link publishes the fully-synced inode without replacing a file
	// that appeared at the destination after newAtomicOutput checked it.
	if err := os.Link(o.temporary, o.target); err != nil {
		return fmt.Errorf("publish export without overwrite: %w", err)
	}
	if err := os.Remove(o.temporary); err != nil {
		return fmt.Errorf("remove temporary export: %w", err)
	}
	if err := syncDirectory(o.directory); err != nil {
		return fmt.Errorf("sync export directory: %w", err)
	}
	return nil
}

func (o *atomicOutput) abort() {
	_ = o.file.Close()
	_ = os.Remove(o.temporary)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path) // #nosec G304 -- path is the explicit local output directory.
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func environmentOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
