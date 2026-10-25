// Command moco-backup creates, verifies and restores full SQLite recovery archives.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/araihu/moco/internal/adapters/backup"
	"github.com/araihu/moco/internal/adapters/encryption"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "moco-backup:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "create" && args[0] != "verify" && args[0] != "restore") {
		return errors.New("usage: moco-backup <create|verify|restore> [flags]")
	}
	command := args[0]
	flags := flag.NewFlagSet("moco-backup "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Duration("timeout", 10*time.Minute, "maximum operation duration")
	var databasePath, archivePath, keyringPath string
	switch command {
	case "create":
		databasePath = os.Getenv("MOCO_DB_PATH")
		if databasePath == "" {
			databasePath = "./moco.db"
		}
		flags.StringVar(&databasePath, "database", databasePath, "existing source SQLite database")
		flags.StringVar(&archivePath, "output", "", "new backup archive file")
	case "verify", "restore":
		flags.StringVar(&archivePath, "backup", "", "backup archive file")
		flags.StringVar(&keyringPath, "keyring", "", "private JSON file in MOCO_ENCRYPTION_KEYS format (required for restore)")
		if command == "restore" {
			flags.StringVar(&databasePath, "database", "", "new offline destination SQLite database")
		}
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || archivePath == "" || *timeout <= 0 || (command != "verify" && databasePath == "") {
		return errors.New("supply the required file paths, a positive timeout and no positional arguments")
	}
	if command == "restore" && keyringPath == "" {
		return errors.New("restore requires --keyring with all root-key eras required by the backup")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var keyring *encryption.Keyring
	if keyringPath != "" {
		parsed, err := readKeyring(keyringPath)
		if err != nil {
			return err
		}
		defer parsed.Destroy()
		keyring = &parsed
	}
	var manifest backup.Manifest
	var err error
	switch command {
	case "create":
		manifest, err = backup.Create(ctx, databasePath, archivePath)
	case "verify":
		manifest, err = backup.Verify(ctx, archivePath, keyring)
	case "restore":
		manifest, err = backup.Restore(ctx, archivePath, databasePath, keyring)
	}
	if err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(manifest); err != nil {
		return fmt.Errorf("operation completed but writing manifest summary failed: %w", err)
	}
	return nil
}

func readKeyring(path string) (encryption.Keyring, error) {
	file, err := os.Open(path) // #nosec G304 -- explicit local deployment keyring input.
	if err != nil {
		return encryption.Keyring{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return encryption.Keyring{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return encryption.Keyring{}, errors.New("keyring must be a regular file without group/world permissions")
	}
	payload, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return encryption.Keyring{}, err
	}
	defer clear(payload)
	return encryption.ParseKeyring(string(payload))
}
