package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/chainworker"
)

func main() {
	cfg := chainworker.LoadConfig()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := chainworker.OpenStore(ctx, cfg)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		logger.Error("migrate database", "error", err)
		os.Exit(1)
	}

	accountPassword, err := secretFromEnv("ACCOUNT_PASSWORD", cfg.AccountPassword)
	if err != nil {
		logger.Error("read account password", "error", err)
		os.Exit(1)
	}
	cfg.AccountPassword = accountPassword
	legacyPassword, err := legacyAccountPassword(cfg)
	if err != nil {
		logger.Error("read legacy account password", "error", err)
		os.Exit(1)
	}
	if legacyPassword == "" {
		legacyPassword = cfg.AccountPassword
	}
	opts := chainworker.LegacyAccountImportOptions{
		Module:                cfg.Module,
		DefaultCrypto:         os.Getenv("LEGACY_ACCOUNT_DEFAULT_CRYPTO"),
		LegacyAccountPassword: legacyPassword,
		AccountPassword:       cfg.AccountPassword,
	}
	legacyDatabaseURL, err := secretFromEnv("LEGACY_ACCOUNTS_DATABASE_URL", "")
	if err != nil {
		logger.Error("read legacy accounts database URL", "error", err)
		os.Exit(1)
	}
	var report chainworker.LegacyAccountImportReport
	if legacyDatabaseURL != "" {
		report, err = chainworker.ImportLegacyAccountsMariaDB(ctx, store, legacyDatabaseURL, opts)
	} else {
		input := os.Stdin
		if len(os.Args) > 1 && os.Args[1] != "-" {
			input, err = os.Open(os.Args[1])
			if err != nil {
				logger.Error("open legacy accounts json", "path", os.Args[1], "error", err)
				os.Exit(1)
			}
			defer input.Close()
		}
		report, err = chainworker.ImportLegacyAccountsJSON(ctx, store, input, opts)
	}
	if err != nil {
		logger.Error("import legacy accounts", "error", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		logger.Error("write report", "error", err)
		os.Exit(1)
	}
}

func legacyAccountPassword(cfg chainworker.Config) (string, error) {
	password, err := secretFromEnv("LEGACY_ACCOUNT_PASSWORD", "")
	if err != nil {
		return "", err
	}
	if password != "" {
		return password, nil
	}
	decryptURL := strings.TrimSpace(os.Getenv("LEGACY_ACCOUNT_DECRYPT_URL"))
	if decryptURL == "" {
		host := strings.TrimSpace(os.Getenv("SHKEEPER_HOST"))
		if host != "" {
			if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
				host = "http://" + host
			}
			decryptURL = strings.TrimRight(host, "/") + "/api/v1/" + cfg.Module + "/decrypt"
		}
	}
	if decryptURL == "" {
		return "", nil
	}
	backendKey, err := secretFromEnv("LEGACY_ACCOUNT_BACKEND_KEY", cfg.BackendKey)
	if err != nil {
		return "", err
	}
	return fetchLegacyAccountPassword(decryptURL, backendKey)
}

func secretFromEnv(name string, fallback string) (string, error) {
	if path := strings.TrimSpace(os.Getenv(name + "_FILE")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, nil
	}
	return fallback, nil
}

func fetchLegacyAccountPassword(rawURL string, backendKey string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	if backendKey != "" {
		req.Header.Set("X-Shkeeper-Backend-Key", backendKey)
	}
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("legacy decrypt endpoint returned %s", resp.Status)
	}
	var payload struct {
		Key              string `json:"key"`
		PersistentStatus string `json:"persistent_status"`
		RuntimeStatus    string `json:"runtime_status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Key) == "" {
		status := strings.TrimSpace(payload.PersistentStatus + "/" + payload.RuntimeStatus)
		if status == "/" {
			status = "unknown"
		}
		return "", errors.New("legacy decrypt endpoint did not return a key; status=" + status)
	}
	return payload.Key, nil
}
