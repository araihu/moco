// Command migrate-sqlite applies Mocó's file-based migrations to a SQLite
// database without linking the unrelated database and source drivers included
// by golang-migrate's generic CLI.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "migrate-sqlite:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("migrate-sqlite", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	migrationsPath := flags.String("path", "", "directory containing file migrations")
	databaseURL := flags.String("database", "", "sqlite:// database URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *migrationsPath == "" || *databaseURL == "" {
		return errors.New("-path and -database are required")
	}
	if flags.NArg() != 1 || flags.Arg(0) != "up" {
		return errors.New("the only supported command is up")
	}

	absPath, err := filepath.Abs(*migrationsPath)
	if err != nil {
		return fmt.Errorf("resolve migration path: %w", err)
	}
	sourceURL := (&url.URL{Scheme: "file", Path: absPath}).String()

	runner, err := migrate.New(sourceURL, *databaseURL)
	if err != nil {
		return fmt.Errorf("initialize migrations: %w", err)
	}
	defer func() {
		_, _ = runner.Close()
	}()

	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
