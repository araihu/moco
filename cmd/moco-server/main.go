package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/araihu/moco/internal/adapters/authn"
	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/adapters/encryption"
	httpapi "github.com/araihu/moco/internal/adapters/http"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
	"github.com/araihu/moco/internal/operations"
	"github.com/google/uuid"
)

var version = "dev"

func main() {
	syscall.Umask(0o077)
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
	if configuration.profile == "production" {
		lock, err := operations.Lock(filepath.Join(filepath.Dir(configuration.databasePath), ".moco-server.lock"))
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
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
	metrics := httpapi.NewMetrics()
	var readiness httpapi.ReadinessChecker = store
	if configuration.profile == "production" {
		if err := store.VerifyVaultKeys(startupContext, envelope); err != nil {
			return fmt.Errorf("verify production vault keys: %w", err)
		}
	}
	auditService, err := services.NewAuditService(store)
	if err != nil {
		return fmt.Errorf("initialize audit service: %w", err)
	}
	var security securityRuntime
	defer func() { security.close() }()
	if err := auditService.RecordMutation(startupContext, func(ctx context.Context) (ports.AuditEvent, error) {
		if _, err := store.EnsureEncryptionKeyState(ctx, configuration.encryptionKeyID, configuration.encryptionKeyEpoch); err != nil {
			return ports.AuditEvent{}, fmt.Errorf("initialize encryption key state: %w", err)
		}
		var err error
		security, err = buildSecurityRuntime(ctx, configuration, store)
		if err != nil {
			return ports.AuditEvent{}, fmt.Errorf("initialize access control: %w", err)
		}
		principal := "system:startup"
		return ports.AuditEvent{OccurredAt: time.Now().UTC(), RequestID: uuid.NewString(), PrincipalID: &principal, Method: "STARTUP", Route: "/internal/startup", StatusCode: 200, Outcome: "success"}, nil
	}); err != nil {
		return fmt.Errorf("commit audited startup: %w", err)
	}
	if configuration.profile == "production" {
		readiness = productionReadiness{store: store, config: configuration, health: metrics.AuditHealthy}
		if err := readiness.Ping(startupContext); err != nil {
			return err
		}
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
	handler, err := httpapi.NewHandler(httpapi.HandlerOptions{
		Tenants:               tenantService,
		Vaults:                vaultService,
		Secrets:               secretService,
		Readiness:             readiness,
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
		AuditResult:           metrics.AuditResult,
	})
	if err != nil {
		return fmt.Errorf("initialize HTTP handler: %w", err)
	}

	handler = metrics.Wrap(handler, logger)
	publicHandler := handler
	if configuration.adminAddress != "" {
		publicHandler = httpapi.PublicListener(handler)
	}
	server := &http.Server{
		Addr:              configuration.address,
		Handler:           publicHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		TLSConfig:         configuration.tlsConfig,
		MaxHeaderBytes:    32 << 10,
	}
	servers := []*http.Server{server}
	if configuration.adminAddress != "" {
		admin := &http.Server{
			Addr: configuration.adminAddress,
			Handler: httpapi.MetricsAuthentication(configuration.metricsToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				metrics.ServePrometheus(w, version)
				operations.ServeDeploymentMetrics(w, r, configuration.databasePath, configuration.backupStatusPath, readiness)
			}), handler),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
			MaxHeaderBytes: 32 << 10,
		}
		if configuration.tlsConfig != nil {
			admin.TLSConfig = configuration.tlsConfig.Clone()
		}
		servers = append(servers, admin)
	}
	defer func() {
		for _, listener := range servers {
			_ = listener.Close()
		}
	}()
	shutdownContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	serverErrors := make(chan error, len(servers))
	policyErrors := make(chan error, 1)
	if security.reloader != nil {
		go func() {
			if err := security.reloader.Run(shutdownContext); err != nil && !errors.Is(err, context.Canceled) {
				policyErrors <- err
			}
		}()
	}
	for _, listener := range servers {
		go func() {
			logger.Info("Mocó server listening", "address", listener.Addr, "version", version, "profile", configuration.profile)
			if listener.TLSConfig != nil {
				serverErrors <- listener.ListenAndServeTLS("", "")
				return
			}
			serverErrors <- listener.ListenAndServe()
		}()
	}

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case err := <-policyErrors:
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		shutdownErr := shutdownServers(ctx, servers)
		if shutdownErr != nil {
			return errors.Join(
				fmt.Errorf("authorization policy reloader stopped: %w", err),
				fmt.Errorf("graceful shutdown after authorization failure: %w", shutdownErr),
			)
		}
		return fmt.Errorf("authorization policy reloader stopped: %w", err)
	case <-shutdownContext.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		if err := shutdownServers(ctx, servers); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}

func shutdownServers(ctx context.Context, servers []*http.Server) error {
	var result error
	for _, server := range servers {
		result = errors.Join(result, server.Shutdown(ctx))
	}
	return result
}

func deriveAuditPathHMACKey(cursorHMACKey string) []byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte("moco/audit-path-hmac/v1\x00"))
	_, _ = hash.Write([]byte(cursorHMACKey))
	return hash.Sum(nil)
}

type configuration struct {
	profile            string
	adminAddress       string
	metricsToken       string
	backupStatusPath   string
	tlsConfig          *tls.Config
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
	transport, err := loadTLSConfiguration(os.Getenv("MOCO_TLS_CERT_FILE"), os.Getenv("MOCO_TLS_KEY_FILE"))
	if err != nil {
		return configuration{}, err
	}
	config := configuration{
		profile:          environmentOrDefault("MOCO_PROFILE", "development"),
		adminAddress:     os.Getenv("MOCO_ADMIN_ADDR"),
		backupStatusPath: os.Getenv("MOCO_BACKUP_STATUS_FILE"),
		tlsConfig:        transport,
		address:          environmentOrDefault("MOCO_ADDR", ":8080"),
		databasePath:     environmentOrDefault("MOCO_DB_PATH", "./moco.db"),
		bearerToken:      os.Getenv("MOCO_BEARER_TOKEN"),
		cursorHMACKey:    os.Getenv("MOCO_CURSOR_HMAC_KEY"),
		authConfigPath:   os.Getenv("MOCO_AUTH_CONFIG"),
		encryptionKeyID:  environmentOrDefault("MOCO_ENCRYPTION_KEY_ID", "local-v1"),
	}
	config.cursorHMACKey, err = credentialEnvironment("MOCO_CURSOR_HMAC_KEY", 4096)
	if err != nil {
		return configuration{}, err
	}
	config.metricsToken, err = credentialEnvironment("MOCO_METRICS_TOKEN", 4096)
	if err != nil {
		return configuration{}, err
	}
	if err := validateProduction(config); err != nil {
		return configuration{}, err
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
	encodedKeys, err := credentialEnvironment("MOCO_ENCRYPTION_KEYS", 64<<10)
	if err != nil {
		return configuration{}, err
	}
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
