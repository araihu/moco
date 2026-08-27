package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/araihu/moco/internal/adapters/db"
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

	tenantService, err := services.NewTenantService(store, services.TenantServiceOptions{
		CursorHMACKey: []byte(configuration.cursorHMACKey),
	})
	if err != nil {
		return fmt.Errorf("initialize tenant service: %w", err)
	}
	handler, err := httpapi.NewHandler(httpapi.HandlerOptions{
		Tenants:        tenantService,
		Readiness:      store,
		BearerToken:    configuration.bearerToken,
		ServiceVersion: version,
		Logger:         logger,
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
	case <-shutdownContext.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}

type configuration struct {
	address       string
	databasePath  string
	bearerToken   string
	cursorHMACKey string
}

func loadConfiguration() (configuration, error) {
	config := configuration{
		address:       environmentOrDefault("MOCO_ADDR", ":8080"),
		databasePath:  environmentOrDefault("MOCO_DB_PATH", "./moco.db"),
		bearerToken:   os.Getenv("MOCO_BEARER_TOKEN"),
		cursorHMACKey: os.Getenv("MOCO_CURSOR_HMAC_KEY"),
	}
	if len(config.bearerToken) < 32 {
		return configuration{}, errors.New("MOCO_BEARER_TOKEN must contain at least 32 bytes")
	}
	if len(config.cursorHMACKey) < 32 {
		return configuration{}, errors.New("MOCO_CURSOR_HMAC_KEY must contain at least 32 bytes")
	}
	return config, nil
}

func environmentOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
