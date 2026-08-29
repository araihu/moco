// Command moco-audit-export writes a finite, read-only audit snapshot as JSONL.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/core/services"
)

const (
	auditExportManifestFormat = "moco.audit-export/v1"
	maxAuditExportLineBytes   = 64 << 10
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
	manifestPath := flags.String("manifest", "", "manifest JSON output path (optional; required by --verify)")
	afterSequence := flags.Int64("after-sequence", 0, "exclusive audit sequence checkpoint")
	verify := flags.Bool("verify", false, "verify an existing JSONL file and manifest without opening SQLite")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*outputPath) == "" {
		return errors.New("output path is required")
	}
	if *afterSequence < 0 {
		return errors.New("after-sequence must not be negative")
	}
	if *verify {
		if *outputPath == "-" {
			return errors.New("verify requires a JSONL file path, not stdout")
		}
		if strings.TrimSpace(*manifestPath) == "" || *manifestPath == "-" {
			return errors.New("verify requires a manifest file path")
		}
		return verifyExport(*outputPath, *manifestPath, stderr)
	}
	if strings.TrimSpace(*databasePath) == "" {
		return errors.New("database path is required")
	}
	if *manifestPath == "-" {
		return errors.New("manifest must be a file path")
	}
	if *manifestPath != "" && *outputPath != "-" && filepath.Clean(*manifestPath) == filepath.Clean(*outputPath) {
		return errors.New("output and manifest paths must be different")
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
	var output *atomicOutput
	if *outputPath != "-" {
		output, err = newAtomicOutput(*outputPath)
		if err != nil {
			return err
		}
	}
	var manifest *atomicOutput
	if *manifestPath != "" {
		manifest, err = newAtomicOutput(*manifestPath)
		if err != nil {
			if output != nil {
				output.abort()
			}
			return err
		}
	}
	if output != nil {
		writer = output.file
	}
	outputCommitted := output == nil
	manifestCommitted := manifest == nil
	defer func() {
		if !outputCommitted {
			output.abort()
		}
		if !manifestCommitted {
			manifest.abort()
		}
	}()
	exportWriter := io.Writer(writer)
	var digest hash.Hash
	if manifest != nil {
		hash := sha256.New()
		digest = hash
		exportWriter = io.MultiWriter(writer, hash)
	}
	result, err := exporter.Export(ctx, exportWriter, services.AuditExportRequest{AfterSequence: *afterSequence})
	if err != nil {
		return fmt.Errorf("export audit events: %w", err)
	}
	if manifest != nil {
		manifestDocument := auditExportManifest{
			Format:        auditExportManifestFormat,
			AfterSequence: result.AfterSequence,
			UpperSequence: result.UpperSequence,
			LastSequence:  result.LastSequence,
			Exported:      result.Exported,
			Complete:      result.Complete,
			JSONLSHA256:   hex.EncodeToString(digest.Sum(nil)),
		}
		if err := writeManifest(manifest.file, manifestDocument); err != nil {
			return fmt.Errorf("write audit export manifest: %w", err)
		}
	}
	if output != nil {
		if err := output.commit(); err != nil {
			return fmt.Errorf("commit audit export: %w", err)
		}
		outputCommitted = true
	}
	if manifest != nil {
		if err := manifest.commit(); err != nil {
			return fmt.Errorf("commit audit export manifest: %w", err)
		}
		manifestCommitted = true
	}
	if _, err := fmt.Fprintf(stderr, "exported %d audit events through sequence %d (snapshot upper %d)\n", result.Exported, result.LastSequence, result.UpperSequence); err != nil {
		return fmt.Errorf("write export summary: %w", err)
	}
	return nil
}

type auditExportManifest struct {
	Format        string `json:"format"`
	AfterSequence int64  `json:"afterSequence"`
	UpperSequence int64  `json:"upperSequence"`
	LastSequence  int64  `json:"lastSequence"`
	Exported      int    `json:"exported"`
	Complete      bool   `json:"complete"`
	JSONLSHA256   string `json:"jsonlSha256"`
}

func writeManifest(writer io.Writer, manifest auditExportManifest) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(manifest)
}

func verifyExport(outputPath, manifestPath string, stderr io.Writer) error {
	manifestFile, err := os.Open(manifestPath) // #nosec G304 -- paths are explicit offline verification inputs.
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	defer func() { _ = manifestFile.Close() }()
	if err := requirePrivateFile(manifestFile, "manifest"); err != nil {
		return err
	}
	var manifest auditExportManifest
	decoder := json.NewDecoder(manifestFile)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("manifest must contain exactly one JSON value")
	}
	if err := validateManifest(manifest); err != nil {
		return err
	}

	jsonlFile, err := os.Open(outputPath) // #nosec G304 -- path is an explicit offline verification input.
	if err != nil {
		return fmt.Errorf("open JSONL export: %w", err)
	}
	defer func() { _ = jsonlFile.Close() }()
	if err := requirePrivateFile(jsonlFile, "JSONL export"); err != nil {
		return err
	}
	hash := sha256.New()
	reader := bufio.NewReaderSize(jsonlFile, 32<<10)
	var count int
	var firstSequence, lastSequence int64
	for {
		rawLine, err := readJSONLLine(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read JSONL export: %w", err)
		}
		if _, err := hash.Write(rawLine); err != nil {
			return fmt.Errorf("hash JSONL export: %w", err)
		}
		line := rawLine[:len(rawLine)-1]
		if len(line) == 0 || line[len(line)-1] == '\r' {
			return errors.New("JSONL export contains an empty or non-canonical line")
		}
		var event services.AuditExportEvent
		lineDecoder := json.NewDecoder(bytes.NewReader(line))
		lineDecoder.DisallowUnknownFields()
		if err := lineDecoder.Decode(&event); err != nil {
			return fmt.Errorf("decode JSONL event %d: %w", count+1, err)
		}
		if err := lineDecoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return fmt.Errorf("JSONL event %d contains multiple JSON values", count+1)
		}
		if err := validateExportEvent(event); err != nil {
			return fmt.Errorf("validate JSONL event %d: %w", count+1, err)
		}
		if count == 0 {
			firstSequence = event.Sequence
		} else if event.Sequence <= lastSequence {
			return fmt.Errorf("JSONL event %d sequence %d is not increasing after %d", count+1, event.Sequence, lastSequence)
		}
		if event.Sequence > manifest.UpperSequence {
			return fmt.Errorf("JSONL event %d sequence %d exceeds manifest upper sequence %d", count+1, event.Sequence, manifest.UpperSequence)
		}
		lastSequence = event.Sequence
		count++
	}
	if count != manifest.Exported {
		return fmt.Errorf("manifest exported=%d but JSONL contains %d events", manifest.Exported, count)
	}
	expectedDigest, err := hex.DecodeString(manifest.JSONLSHA256)
	if err != nil || !bytes.Equal(hash.Sum(nil), expectedDigest) {
		return errors.New("JSONL checksum does not match manifest")
	}
	if count == 0 {
		if manifest.LastSequence != manifest.AfterSequence {
			return errors.New("empty JSONL export has an unexpected last sequence")
		}
	} else {
		if firstSequence <= manifest.AfterSequence || lastSequence != manifest.LastSequence {
			return errors.New("JSONL sequence bounds do not match manifest")
		}
	}
	if _, err := fmt.Fprintf(stderr, "verified %d audit events through sequence %d (snapshot upper %d)\n", count, manifest.LastSequence, manifest.UpperSequence); err != nil {
		return fmt.Errorf("write verification summary: %w", err)
	}
	return nil
}

func readJSONLLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		line = append(line, fragment...)
		if len(line) > maxAuditExportLineBytes {
			return nil, fmt.Errorf("JSONL line exceeds %d bytes", maxAuditExportLineBytes)
		}
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, errors.New("JSONL export must end each record with a newline")
		}
		return nil, err
	}
}

func validateManifest(manifest auditExportManifest) error {
	if manifest.Format != auditExportManifestFormat {
		return fmt.Errorf("unsupported manifest format %q", manifest.Format)
	}
	if manifest.AfterSequence < 0 || manifest.UpperSequence < 0 || manifest.LastSequence < 0 {
		return errors.New("manifest sequence values must not be negative")
	}
	if manifest.AfterSequence > manifest.UpperSequence || manifest.LastSequence < manifest.AfterSequence || manifest.LastSequence > manifest.UpperSequence {
		return errors.New("manifest sequence bounds are inconsistent")
	}
	if manifest.Exported < 0 || !manifest.Complete {
		return errors.New("manifest must describe a complete non-negative export")
	}
	if len(manifest.JSONLSHA256) != sha256.Size*2 {
		return errors.New("manifest JSONL checksum must be 64 hexadecimal characters")
	}
	digest, err := hex.DecodeString(manifest.JSONLSHA256)
	if err != nil || hex.EncodeToString(digest) != manifest.JSONLSHA256 {
		return errors.New("manifest JSONL checksum must be lowercase hexadecimal")
	}
	return nil
}

func validateExportEvent(event services.AuditExportEvent) error {
	if event.Sequence < 1 || event.OccurredAt.IsZero() || event.RequestID == "" || len(event.RequestID) > 128 {
		return errors.New("event identity or occurrence is invalid")
	}
	if event.PrincipalID != nil && (*event.PrincipalID == "" || len(*event.PrincipalID) > 128) {
		return errors.New("event principal is invalid")
	}
	if len(event.Method) < 1 || len(event.Method) > 16 || len(event.Route) < 1 || len(event.Route) > 2048 {
		return errors.New("event method or route is invalid")
	}
	if event.StatusCode < 100 || event.StatusCode > 599 || (event.Outcome != "success" && event.Outcome != "failure") {
		return errors.New("event status or outcome is invalid")
	}
	if event.SecretPathSHA256 != nil {
		if len(*event.SecretPathSHA256) != sha256.Size*2 {
			return errors.New("event secret path checksum is invalid")
		}
		decoded, err := hex.DecodeString(*event.SecretPathSHA256)
		if err != nil || hex.EncodeToString(decoded) != *event.SecretPathSHA256 {
			return errors.New("event secret path checksum must be lowercase hexadecimal")
		}
	}
	return nil
}

func requirePrivateFile(file *os.File, label string) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", label)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must not be group/world-readable (mode %o)", label, info.Mode().Perm())
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
