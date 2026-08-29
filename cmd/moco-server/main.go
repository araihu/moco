package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/araihu/moco/internal/adapters/authn"
	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/adapters/encryption"
	httpapi "github.com/araihu/moco/internal/adapters/http"
	"github.com/araihu/moco/internal/core/services"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configuration, err := loadConfiguration()
	if err != nil {
		return err
	}
	envelope, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: configuration.encryptionKeyID,
		RootKeys:  configuration.encryptionKeys,
	})
	wipeConfigurationKey(configuration.encryptionKey)
	wipeConfigurationKeys(configuration.encryptionKeys)
	if err != nil {
		return fmt.Errorf("initialize secret encryption: %w", err)
	}
	defer envelope.Destroy()

	startupContext, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStartup()
	store, err := db.Open(startupContext, configuration.databasePath)
	if err != nil {
		return fmt.Errorf("initialize persistence: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("close database", "error", err)
		}
	}()
	if _, err := store.EnsureEncryptionKeyState(startupContext, configuration.encryptionKeyID, configuration.encryptionKeyEpoch); err != nil {
		return fmt.Errorf("initialize encryption key state: %w", err)
	}

	tenantService, err := services.NewTenantService(store, services.TenantServiceOptions{
		CursorHMACKey: []byte(configuration.cursorHMACKey),
	})
	if err != nil {
		return fmt.Errorf("initialize tenant service: %w", err)
	}
	vaultService, err := services.NewVaultService(store, services.VaultServiceOptions{
		CursorHMACKey: []byte(configuration.cursorHMACKey),
	})
	if err != nil {
		return fmt.Errorf("initialize vault service: %w", err)
	}
	secretService, err := services.NewSecretService(store, envelope, services.SecretServiceOptions{
		CursorHMACKey: []byte(configuration.cursorHMACKey),
		KeyState:      store,
	})
	if err != nil {
		return fmt.Errorf("initialize secret service: %w", err)
	}
	auditService, err := services.NewAuditService(store)
	if err != nil {
		return fmt.Errorf("initialize audit service: %w", err)
	}
	auditRetentionService, err := services.NewAuditRetentionService(store)
	if err != nil {
		return fmt.Errorf("initialize audit retention service: %w", err)
	}
	auditPathHMACKey := deriveAuditPathHMACKey(configuration.cursorHMACKey)
	defer wipeConfigurationKey(auditPathHMACKey)
	keyRotationService, err := services.NewVaultKeyRotationService(store, envelope, services.VaultKeyRotationServiceOptions{KeyState: store})
	if err != nil {
		return fmt.Errorf("initialize vault key rotation service: %w", err)
	}
	security, err := buildSecurityRuntime(startupContext, configuration, store)
	if err != nil {
		return fmt.Errorf("initialize access control: %w", err)
	}
	defer security.close()
	handler, err := httpapi.NewHandler(httpapi.HandlerOptions{
		Tenants:               tenantService,
		Vaults:                vaultService,
		Secrets:               secretService,
		Readiness:             store,
		ResourceVersion:       store,
		TenantResourceVersion: store,
		Authenticator:         security.authenticator,
		Authorizer:            security.authorizer,
		Authorization:         security.policyService,
		Audit:                 auditService,
		AuditRetention:        auditRetentionService,
		AuditPathHMACKey:      auditPathHMACKey,
		KeyRotation:           keyRotationService,
		PrincipalCheck:        security.authenticator.HasPrincipal,
		ServiceVersion:        version,
		Logger:                logger,
	})
	if err != nil {
		return fmt.Errorf("initialize HTTP handler: %w", err)
	}

	server := &http.Server{
		Addr:              configuration.address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	shutdownContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	serverErrors := make(chan error, 1)
	policyErrors := make(chan error, 1)
	if security.reloader != nil {
		go func() {
			if err := security.reloader.Run(shutdownContext); err != nil && !errors.Is(err, context.Canceled) {
				policyErrors <- err
			}
		}()
	}
	go func() {
		logger.Info("Mocó server listening", "address", configuration.address, "version", version)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case err := <-policyErrors:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(ctx)
		if shutdownErr != nil {
			return errors.Join(
				fmt.Errorf("authorization policy reloader stopped: %w", err),
				fmt.Errorf("graceful shutdown after authorization failure: %w", shutdownErr),
			)
		}
		return fmt.Errorf("authorization policy reloader stopped: %w", err)
	case <-shutdownContext.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}

func deriveAuditPathHMACKey(cursorHMACKey string) []byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte("moco/audit-path-hmac/v1\x00"))
	_, _ = hash.Write([]byte(cursorHMACKey))
	return hash.Sum(nil)
}

type configuration struct {
	address            string
	databasePath       string
	bearerToken        string
	cursorHMACKey      string
	authConfigPath     string
	encryptionKeyID    string
	encryptionKey      []byte
	encryptionKeys     map[string][]byte
	encryptionKeyEpoch int64
}

func loadConfiguration() (configuration, error) {
	config := configuration{
		address:         environmentOrDefault("MOCO_ADDR", ":8080"),
		databasePath:    environmentOrDefault("MOCO_DB_PATH", "./moco.db"),
		bearerToken:     os.Getenv("MOCO_BEARER_TOKEN"),
		cursorHMACKey:   os.Getenv("MOCO_CURSOR_HMAC_KEY"),
		authConfigPath:  os.Getenv("MOCO_AUTH_CONFIG"),
		encryptionKeyID: environmentOrDefault("MOCO_ENCRYPTION_KEY_ID", "local-v1"),
	}
	epoch, err := strconv.ParseInt(environmentOrDefault("MOCO_ENCRYPTION_KEY_EPOCH", "1"), 10, 64)
	if err != nil || epoch < 1 {
		return configuration{}, errors.New("MOCO_ENCRYPTION_KEY_EPOCH must be a positive decimal integer")
	}
	config.encryptionKeyEpoch = epoch
	if config.authConfigPath != "" && config.bearerToken != "" {
		return configuration{}, errors.New("MOCO_AUTH_CONFIG and MOCO_BEARER_TOKEN are mutually exclusive")
	}
	if config.authConfigPath == "" && len(config.bearerToken) < 32 {
		return configuration{}, errors.New("MOCO_BEARER_TOKEN must contain at least 32 bytes")
	}
	if len(config.cursorHMACKey) < 32 {
		return configuration{}, errors.New("MOCO_CURSOR_HMAC_KEY must contain at least 32 bytes")
	}
	if !validEncryptionKeyID(config.encryptionKeyID) {
		return configuration{}, errors.New("MOCO_ENCRYPTION_KEY_ID must contain 1 to 128 visible ASCII characters")
	}
	encodedKey := os.Getenv("MOCO_ENCRYPTION_KEY")
	encodedKeys := os.Getenv("MOCO_ENCRYPTION_KEYS")
	if encodedKey != "" && encodedKeys != "" {
		return configuration{}, errors.New("MOCO_ENCRYPTION_KEY and MOCO_ENCRYPTION_KEYS are mutually exclusive")
	}
	if encodedKeys != "" {
		keyring, err := loadEncryptionKeyring(encodedKeys)
		if err != nil {
			return configuration{}, err
		}
		config.encryptionKeyID = keyring.ActiveKeyID
		config.encryptionKeys = keyring.Keys
		config.encryptionKey = append([]byte(nil), keyring.Keys[keyring.ActiveKeyID]...)
		return config, nil
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		wipeConfigurationKey(key)
		return configuration{}, errors.New("MOCO_ENCRYPTION_KEY must be standard base64 encoding of exactly 32 bytes")
	}
	config.encryptionKey = key
	config.encryptionKeys = map[string][]byte{config.encryptionKeyID: append([]byte(nil), key...)}
	return config, nil
}

var _ httpapi.BearerAuthenticator = (*authn.TokenAuthenticator)(nil)

func environmentOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func wipeConfigurationKey(value []byte) {
	clear(value)
	runtime.KeepAlive(value)
}

func wipeConfigurationKeys(values map[string][]byte) {
	for keyID, value := range values {
		wipeConfigurationKey(value)
		delete(values, keyID)
	}
}
