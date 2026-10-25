package main

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"

	"github.com/araihu/moco/internal/adapters/db"
	httpapi "github.com/araihu/moco/internal/adapters/http"
	"github.com/araihu/moco/internal/operations"
)

func productionConfiguration(t *testing.T) configuration {
	t.Helper()
	root := t.TempDir()
	credentials := filepath.Join(root, "credentials")
	if err := operations.Initialize(credentials); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil { // #nosec G302 -- private directory requires execute permission.
		t.Fatal(err)
	}
	keyPath := filepath.Join(credentials, "tls.key")
	if err := os.WriteFile(keyPath, []byte("private test key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCO_TLS_KEY_FILE", keyPath)
	for name, file := range map[string]string{"MOCO_ENCRYPTION_KEYS": "keyring.json", "MOCO_CURSOR_HMAC_KEY": "cursor-key", "MOCO_METRICS_TOKEN": "metrics-token"} {
		t.Setenv(name, "")
		t.Setenv(name+"_FILE", filepath.Join(credentials, file))
	}
	return configuration{profile: "production", tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12}, address: ":8443", adminAddress: ":9443", databasePath: filepath.Join(root, "moco.db"), authConfigPath: filepath.Join(credentials, "access.json"), metricsToken: "test-metrics-token-with-at-least-32-bytes", backupStatusPath: filepath.Join(root, "status.json"), encryptionKeyID: "root-v1", encryptionKeyEpoch: 1}
}

func TestProductionRejectsUnsafeConfiguration(t *testing.T) {
	config := productionConfiguration(t)
	if err := validateProduction(config); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*configuration){
		"no TLS":                  func(c *configuration) { c.tlsConfig = nil },
		"legacy auth":             func(c *configuration) { c.bearerToken = "legacy" },
		"missing principals":      func(c *configuration) { c.authConfigPath = "" },
		"public administration":   func(c *configuration) { c.adminAddress = c.address },
		"missing admin listener":  func(c *configuration) { c.adminAddress = "" },
		"relative database":       func(c *configuration) { c.databasePath = "moco.db" },
		"missing backup status":   func(c *configuration) { c.backupStatusPath = "" },
		"weak metrics credential": func(c *configuration) { c.metricsToken = "weak" },
		"unknown profile":         func(c *configuration) { c.profile = "prodution" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := config
			change(&candidate)
			if err := validateProduction(candidate); err == nil {
				t.Fatal("unsafe production configuration accepted")
			}
		})
	}
	t.Setenv("MOCO_CURSOR_HMAC_KEY", "inline-secret-must-be-rejected")
	if err := validateProduction(config); err == nil {
		t.Fatal("inline production secret accepted")
	}
	if _, err := credentialEnvironment("MOCO_CURSOR_HMAC_KEY", 4096); err == nil {
		t.Fatal("ambiguous credential sources accepted")
	}
	t.Setenv("MOCO_CURSOR_HMAC_KEY", "")
	if err := os.Chmod(config.authConfigPath, 0o644); err != nil { // #nosec G302 -- negative test: public credentials must fail production startup.
		t.Fatal(err)
	}
	if err := validateProduction(config); err == nil {
		t.Fatal("world-readable production access configuration accepted")
	}
}

func TestProductionReadinessDetectsEpochChangesAndAuditIncident(t *testing.T) {
	config := productionConfiguration(t)
	store, err := db.Open(t.Context(), config.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.EnsureEncryptionKeyState(t.Context(), "root-v1", 1); err != nil {
		t.Fatal(err)
	}
	metrics := httpapi.NewMetrics()
	readiness := productionReadiness{store, config, metrics.AuditHealthy}
	if err := readiness.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureEncryptionKeyState(t.Context(), "root-v2", 2); err != nil {
		t.Fatal(err)
	}
	if err := readiness.Ping(t.Context()); err == nil {
		t.Fatal("stale encryption epoch reported ready")
	}
	readiness.config.encryptionKeyID, readiness.config.encryptionKeyEpoch = "root-v2", 2
	if err := readiness.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	metrics.AuditResult(os.ErrPermission)
	if err := readiness.Ping(t.Context()); err == nil {
		t.Fatal("audit incident reported ready")
	}
}
