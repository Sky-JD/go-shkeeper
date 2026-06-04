package main

import (
	"context"
	"encoding/json"
	"errors"
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
	Status           string   `json:"status"`
	UpdatedAt        string   `json:"updated_at"`
	Cryptos          []string `json:"cryptos"`
	Username         string   `json:"username,omitempty"`
	UpdatedCount     int      `json:"updated_count"`
	CredentialsSaved bool     `json:"credentials_saved"`
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

	key, username, err := serverKeyFromEnv()
	if err != nil {
		logger.Error("read worker serverkey", "error", err)
		os.Exit(1)
	}
	cryptos, err := targetCryptos(ctx, store)
	if err != nil {
		logger.Error("resolve target cryptos", "error", err)
		os.Exit(1)
	}
	for _, crypto := range cryptos {
		if err := store.UpdateWalletServerKey(ctx, crypto, key); err != nil {
			logger.Error("update wallet serverkey", "crypto", crypto, "error", err)
			os.Exit(1)
		}
	}
	rep := report{
		Status:           "ok",
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
		Cryptos:          cryptos,
		Username:         username,
		UpdatedCount:     len(cryptos),
		CredentialsSaved: true,
	}
	if path := firstEnv("WORKER_SERVERKEY_REPORT_FILE", "SERVERKEY_REPORT_FILE"); path != "" {
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

func serverKeyFromEnv() (string, string, error) {
	if key, err := secretFromEnv("WORKER_SERVERKEY"); err != nil {
		return "", "", err
	} else if key != "" {
		user, _, _ := strings.Cut(key, ":")
		return key, strings.TrimSpace(user), nil
	}
	username := strings.TrimSpace(firstEnv("WORKER_USERNAME", "WORKER_SERVERKEY_USERNAME"))
	password, err := secretFromEnv("WORKER_PASSWORD")
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(password) == "" {
		password, err = secretFromEnv("WORKER_SERVERKEY_PASSWORD")
		if err != nil {
			return "", "", err
		}
	}
	if username == "" || strings.TrimSpace(password) == "" {
		return "", "", errors.New("set WORKER_SERVERKEY or WORKER_USERNAME plus WORKER_PASSWORD")
	}
	return username + ":" + strings.TrimSpace(password), username, nil
}

func targetCryptos(ctx context.Context, store *app.Store) ([]string, error) {
	if explicit := splitCSV(firstEnv("WORKER_SERVERKEY_CRYPTOS", "WORKER_CRYPTOS")); len(explicit) > 0 {
		return explicit, nil
	}
	wallets, err := store.ListWallets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(wallets))
	for _, wallet := range wallets {
		if wallet.Enabled && strings.TrimSpace(wallet.Crypto) != "" {
			out = append(out, strings.ToUpper(strings.TrimSpace(wallet.Crypto)))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no enabled wallets found; set WORKER_SERVERKEY_CRYPTOS to update explicit wallets")
	}
	return dedupe(out), nil
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

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToUpper(strings.TrimSpace(part))
		if part != "" {
			out = append(out, part)
		}
	}
	return dedupe(out)
}

func dedupe(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
