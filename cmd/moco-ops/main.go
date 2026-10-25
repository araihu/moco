// Command moco-ops provides credential bootstrap, backup scheduling and probes.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/araihu/moco/internal/operations"
	"github.com/google/uuid"
)

func main() {
	syscall.Umask(0o077)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "moco-ops:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: moco-ops <init|init-mirror|prepare-runtime|backup-once|backup-loop|backup-health|probe> [flags]")
	}
	flags := flag.NewFlagSet("moco-ops "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	switch args[0] {
	case "prepare-runtime":
		options := operations.RuntimeOptions{}
		flags.StringVar(&options.Source, "source", "/projected", "projected credential directory")
		flags.StringVar(&options.Credentials, "credentials", "/runtime/credentials", "private credential copies")
		flags.StringVar(&options.Data, "data", "/storage/data/moco", "dedicated private SQLite directory")
		flags.StringVar(&options.Backups, "backups", "/storage/backups/moco", "dedicated private archive directory")
		flags.StringVar(&options.Mirror, "mirror", "/storage/mirror/moco", "previously provisioned mirror directory")
		flags.StringVar(&options.MirrorID, "mirror-id", "", "previously provisioned destination UUID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("prepare-runtime accepts no positional arguments")
		}
		return operations.PrepareRuntime(options)
	case "init":
		directory := flags.String("directory", "", "new private credential directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *directory == "" || flags.NArg() != 0 {
			return errors.New("init requires --directory and no positional arguments")
		}
		return operations.Initialize(*directory)
	case "init-mirror":
		directory := flags.String("directory", "/mirror", "provisioned private off-host archive directory")
		id := flags.String("id", uuid.NewString(), "nonzero destination UUID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("init-mirror accepts no positional arguments")
		}
		if err := operations.InitializeMirror(*directory, *id); err != nil {
			return err
		}
		_, err := fmt.Fprintln(stdout, *id)
		return err
	case "backup-once", "backup-loop":
		options := operations.BackupOptions{}
		flags.StringVar(&options.Database, "database", os.Getenv("MOCO_DB_PATH"), "live SQLite file")
		flags.StringVar(&options.Directory, "directory", "/backups", "private local backup directory")
		flags.StringVar(&options.MirrorDirectory, "mirror-directory", "/mirror", "private separately provisioned off-host filesystem")
		flags.StringVar(&options.MirrorID, "mirror-id", os.Getenv("MOCO_MIRROR_ID"), "expected off-host destination UUID")
		flags.StringVar(&options.Keyring, "keyring", "/run/moco/keyring.json", "private recovery keyring file")
		flags.IntVar(&options.Keep, "keep", 96, "archives retained at each destination (minimum two)")
		interval := flags.Duration("interval", 15*time.Minute, "delay between backup starts; one job at a time")
		timeout := flags.Duration("timeout", 10*time.Minute, "deadline per backup job")
		waitReady := flags.Bool("wait-ready", false, "wait for the local TLS server before the first backup")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *timeout <= 0 || *interval <= *timeout || options.Keep < 2 {
			return errors.New("require positive timeout, interval greater than timeout, keep >= 2, and no positional arguments")
		}
		if *waitReady {
			for probe(ctx, "https://localhost:8443/readyz", "/run/moco/ca.pem") != nil {
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		logger := slog.New(slog.NewJSONHandler(stderr, nil))
		runJob := func() (operations.BackupStatus, error) {
			job, cancel := context.WithTimeout(ctx, *timeout)
			defer cancel()
			return operations.RunBackup(job, options)
		}
		if args[0] == "backup-once" {
			status, err := runJob()
			if err != nil {
				return err
			}
			return json.NewEncoder(stdout).Encode(status)
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			started := time.Now()
			status, err := runJob()
			if err != nil {
				logger.Error("backup job failed", "error", err)
			} else {
				logger.Info("backup job completed", "snapshotAt", status.SnapshotAt, "durationSeconds", status.DurationSeconds)
			}
			timer := time.NewTimer(max(0, *interval-time.Since(started)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	case "backup-health":
		path := flags.String("status", "/backups/status.json", "backup status file")
		maxAge := flags.Duration("max-age", 30*time.Minute, "maximum snapshot age")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *maxAge <= 0 {
			return errors.New("require a positive max-age and no positional arguments")
		}
		status, err := operations.ReadBackupStatus(*path)
		if err != nil {
			return err
		}
		age := time.Since(status.SnapshotAt)
		if !status.LastAttemptOK || status.SnapshotAt.IsZero() || age < 0 || age > *maxAge {
			return errors.New("last backup failed or its snapshot is overdue")
		}
		return nil
	case "probe":
		url := flags.String("url", "https://localhost:8443/readyz", "HTTPS readiness URL")
		caPath := flags.String("ca", "/run/moco/ca.pem", "trusted CA PEM file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("probe accepts no positional arguments")
		}
		return probe(ctx, *url, *caPath)
	default:
		return errors.New("unknown operation")
	}
}

func probe(ctx context.Context, url, caPath string) error {
	ca, err := os.ReadFile(caPath) // #nosec G304 G703 -- local operator explicitly chooses the deployment CA file; no remote input.
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("invalid CA bundle")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) // #nosec G704 -- local operator explicitly chooses the health target; no remote request input.
	if err != nil {
		return err
	}
	if request.URL.Scheme != "https" {
		return errors.New("probe requires HTTPS")
	}
	response, err := client.Do(request) // #nosec G704 -- probing the operator-selected HTTPS target is this command's purpose; redirects are disabled.
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}
