package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/operations"
)

func credentialEnvironment(name string, limit int64) (string, error) {
	value, path := os.Getenv(name), os.Getenv(name+"_FILE")
	if path == "" {
		return value, nil
	}
	if value != "" {
		return "", fmt.Errorf("%s and %s_FILE are mutually exclusive", name, name)
	}
	payload, err := operations.ReadPrivateFile(path, limit)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", name, err)
	}
	defer clear(payload)
	return strings.TrimSpace(string(payload)), nil
}

func validateProduction(config configuration) error {
	if config.profile != "development" && config.profile != "production" {
		return errors.New("MOCO_PROFILE must be development or production")
	}
	if config.profile != "production" {
		return nil
	}
	if config.tlsConfig == nil || config.authConfigPath == "" || config.bearerToken != "" || config.adminAddress == "" || config.adminAddress == config.address {
		return errors.New("production requires native TLS, MOCO_AUTH_CONFIG and a separate MOCO_ADMIN_ADDR")
	}
	for _, address := range []string{config.address, config.adminAddress} {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return errors.New("production listener addresses must include a host and port")
		}
	}
	for _, name := range []string{"MOCO_ENCRYPTION_KEYS", "MOCO_CURSOR_HMAC_KEY", "MOCO_METRICS_TOKEN"} {
		if os.Getenv(name) != "" || os.Getenv(name+"_FILE") == "" {
			return fmt.Errorf("production requires %s_FILE instead of inline credentials", name)
		}
	}
	for _, path := range []string{config.authConfigPath, os.Getenv("MOCO_TLS_KEY_FILE")} {
		payload, err := operations.ReadPrivateFile(path, 1<<20)
		clear(payload)
		if err != nil {
			return fmt.Errorf("production credential permissions: %w", err)
		}
	}
	if len(config.metricsToken) < 32 {
		return errors.New("MOCO_METRICS_TOKEN_FILE must contain at least 32 bytes")
	}
	if !filepath.IsAbs(config.databasePath) {
		return errors.New("production requires an absolute MOCO_DB_PATH on local storage")
	}
	info, err := os.Stat(filepath.Dir(config.databasePath))
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("production database parent must exist with private directory permissions (0700)")
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Lstat(config.databasePath + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("production database and sidecars must be private regular files")
		}
	}
	if !filepath.IsAbs(config.backupStatusPath) {
		return errors.New("production requires an absolute MOCO_BACKUP_STATUS_FILE")
	}
	return nil
}

type productionReadiness struct {
	store  *db.Store
	config configuration
	health func() bool
}

func (p productionReadiness) Ping(ctx context.Context) error {
	if !p.health() {
		return errors.New("audit persistence is unhealthy")
	}
	state, err := p.store.CurrentEncryptionKeyState(ctx)
	if err != nil {
		return err
	}
	if state.ActiveRootKeyID != p.config.encryptionKeyID || state.Epoch != p.config.encryptionKeyEpoch {
		return errors.New("configured encryption epoch is stale")
	}
	return p.store.Ping(ctx)
}
