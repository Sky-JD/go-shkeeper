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
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/app"
)

type report struct {
	Status          string `json:"status"`
	UpdatedAt       string `json:"updated_at"`
	UserID          int64  `json:"user_id"`
	Username        string `json:"username"`
	PasswordUpdated bool   `json:"password_updated"`
}

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

	password, err := secretFromEnv("ADMIN_PASSWORD")
	if err != nil {
		logger.Error("read admin password", "error", err)
		os.Exit(1)
	}
	if strings.TrimSpace(password) == "" {
		logger.Error("read admin password", "error", "ADMIN_PASSWORD or ADMIN_PASSWORD_FILE is required")
		os.Exit(1)
	}
	user, err := store.FirstUser(ctx)
	if err != nil {
		logger.Error("load admin user", "error", err)
		os.Exit(1)
	}
	username := strings.TrimSpace(firstEnv("ADMIN_USERNAME", "ADMIN_ACCOUNT_USERNAME"))
	if username == "" {
		username = strings.TrimSpace(user.Username)
	}
	if username == "" {
		username = "admin"
	}

	hash, err := app.NewAuthManager(cfg, store, logger).HashPassword(password)
	if err != nil {
		logger.Error("hash admin password", "error", err)
		os.Exit(1)
	}
	if err := store.UpdateAdminAccount(ctx, user.ID, username, hash); err != nil {
		logger.Error("update admin account", "error", err)
		os.Exit(1)
	}
	rep := report{
		Status:          "ok",
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
		UserID:          user.ID,
		Username:        username,
		PasswordUpdated: true,
	}
	if path := os.Getenv("ADMIN_ACCOUNT_REPORT_FILE"); path != "" {
		if err := writeReport(path, rep); err != nil {
			logger.Error("write report file", "path", path, "error", err)
			os.Exit(1)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(rep); err != nil {
		logger.Error("write report", "error", err)
		os.Exit(1)
	}
}

func writeReport(path string, rep report) error {
	data, err := json.MarshalIndent(rep, "", "  ")
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

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}
