package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		logger.Error("migrate database", "error", err)
		os.Exit(1)
	}

	input := os.Stdin
	if len(os.Args) > 1 && os.Args[1] != "-" {
		input, err = os.Open(os.Args[1])
		if err != nil {
			logger.Error("open legacy json", "path", os.Args[1], "error", err)
			os.Exit(1)
		}
		defer input.Close()
	}
	report, err := app.ImportLegacyJSON(ctx, store, input)
	if err != nil {
		logger.Error("import legacy json", "error", err)
		os.Exit(1)
	}
	if path := os.Getenv("IMPORT_LEGACY_REPORT_FILE"); path != "" {
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
