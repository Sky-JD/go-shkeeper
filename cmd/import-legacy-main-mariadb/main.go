package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Sky-JD/go-shkeeper/internal/app"
)

func main() {
	cfg := app.LoadConfig()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := app.OpenStore(ctx, cfg, logger)
	if err != nil {
		logger.Error("open target database", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		logger.Error("migrate target database", "error", err)
		os.Exit(1)
	}

	legacyURL, err := secretFromEnv("LEGACY_MAIN_DATABASE_URL")
	if err != nil {
		logger.Error("read legacy main database URL", "error", err)
		os.Exit(1)
	}
	report, err := app.ImportLegacyMariaDB(ctx, store, legacyURL)
	if err != nil {
		logger.Error("import legacy main mariadb", "error", err)
		os.Exit(1)
	}
	if path := firstEnv("IMPORT_LEGACY_MAIN_REPORT_FILE", "IMPORT_LEGACY_REPORT_FILE"); path != "" {
		if err := writeReport(path, report); err != nil {
			logger.Error("write report file", "path", path, "error", err)
			os.Exit(1)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		logger.Error("write report", "error", err)
		os.Exit(1)
	}
}

func secretFromEnv(name string) (string, error) {
	if path := strings.TrimSpace(os.Getenv(name + "_FILE")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return strings.TrimSpace(os.Getenv(name)), nil
}

func writeReport(path string, report app.LegacyImportReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o600)
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}
