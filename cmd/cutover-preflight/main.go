package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Sky-JD/go-shkeeper/internal/preflight"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := preflight.LoadConfigFromEnv()
	report, err := preflight.Run(ctx, cfg, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cutover-preflight failed:", err)
		os.Exit(1)
	}
	if err := preflight.WriteReport(cfg.OutputFile, report); err != nil {
		fmt.Fprintln(os.Stderr, "cutover-preflight report failed:", err)
		os.Exit(1)
	}
	if cfg.OutputFile == "" {
		_ = json.NewEncoder(os.Stdout).Encode(report)
	}
	if err := preflight.ErrorForStatus(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
