package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/app"
)

func main() {
	cfg := app.LoadConfig()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := app.OpenStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	if cfg.MigrateOnStart {
		if err := store.Migrate(ctx); err != nil {
			logger.Error("migrate database", "error", err)
			os.Exit(1)
		}
	} else {
		logger.Info("startup database migration disabled by SHKEEPER_MIGRATE_ON_START")
	}

	registry := app.NewCryptoRegistry(cfg, store, logger)
	if cfg.EnsureCurrenciesOnStart {
		if err := registry.EnsureCurrencies(ctx); err != nil {
			logger.Error("register currencies", "error", err)
			os.Exit(1)
		}
	} else {
		logger.Info("startup currency registration disabled by SHKEEPER_ENSURE_CURRENCIES_ON_START")
	}

	auth := app.NewAuthManager(cfg, store, logger)
	rateService := app.NewRateService(cfg, store, logger)
	handler := app.NewHTTPHandler(cfg, store, registry, rateService, auth, logger)

	scheduler := app.NewScheduler(cfg, store, registry, rateService, logger)
	scheduler.Start(ctx)
	defer scheduler.Stop()

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("go-shkeeper listening", "addr", cfg.ListenAddr, "database", cfg.DatabaseDriver)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", err)
			os.Exit(1)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", "error", err)
		os.Exit(1)
	}
}
