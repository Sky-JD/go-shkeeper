package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sky-JD/go-shkeeper/internal/chainworker"
)

func TestLegacyAccountPasswordFetchesDecryptEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Shkeeper-Backend-Key") != "backend-key" {
			t.Fatalf("missing backend key header")
		}
		_, _ = w.Write([]byte(`{"persistent_status":"disabled","runtime_status":"success","key":"legacy-key"}`))
	}))
	defer server.Close()

	t.Setenv("LEGACY_ACCOUNT_PASSWORD", "")
	t.Setenv("LEGACY_ACCOUNT_PASSWORD_FILE", "")
	t.Setenv("LEGACY_ACCOUNT_DECRYPT_URL", server.URL)
	t.Setenv("LEGACY_ACCOUNT_BACKEND_KEY", "backend-key")
	t.Setenv("LEGACY_ACCOUNT_BACKEND_KEY_FILE", "")
	t.Setenv("SHKEEPER_HOST", "")

	got, err := legacyAccountPassword(chainworker.Config{Module: "BNB"})
	if err != nil {
		t.Fatalf("legacyAccountPassword error: %v", err)
	}
	if got != "legacy-key" {
		t.Fatalf("unexpected password: %q", got)
	}
}

func TestConfiguredLegacyAccountPasswordDoesNotFetchDecryptEndpoint(t *testing.T) {
	t.Setenv("LEGACY_ACCOUNT_PASSWORD", "")
	t.Setenv("LEGACY_ACCOUNT_PASSWORD_FILE", "")
	t.Setenv("LEGACY_ACCOUNT_DECRYPT_URL", "http://127.0.0.1:1/decrypt")
	t.Setenv("SHKEEPER_HOST", "127.0.0.1")

	got, explicit, err := configuredLegacyAccountPassword()
	if err != nil {
		t.Fatalf("configuredLegacyAccountPassword error: %v", err)
	}
	if got != "" || explicit {
		t.Fatalf("unexpected configured password: got=%q explicit=%v", got, explicit)
	}
}

func TestShouldRetryWithLegacyAccountPassword(t *testing.T) {
	if !shouldRetryWithLegacyAccountPassword(errors.New("address 0x1: legacy account password is required")) {
		t.Fatalf("expected missing legacy password error to retry")
	}
	if !shouldRetryWithLegacyAccountPassword(errors.New("address 0x1: legacy fernet token signature mismatch")) {
		t.Fatalf("expected legacy fernet error to retry")
	}
	if shouldRetryWithLegacyAccountPassword(errors.New("crypto is required")) {
		t.Fatalf("unexpected retry for unrelated import error")
	}
}

func TestSecretFromEnvPrefersFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte(" file-secret \n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	t.Setenv("ACCOUNT_PASSWORD", "env-secret")
	t.Setenv("ACCOUNT_PASSWORD_FILE", path)

	got, err := secretFromEnv("ACCOUNT_PASSWORD", "fallback")
	if err != nil {
		t.Fatalf("secretFromEnv error: %v", err)
	}
	if got != "file-secret" {
		t.Fatalf("unexpected password: %q", got)
	}
}

func TestFetchLegacyAccountPasswordRequiresKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"persistent_status":"enabled","runtime_status":"pending"}`))
	}))
	defer server.Close()

	if _, err := fetchLegacyAccountPassword(server.URL, "backend-key"); err == nil {
		t.Fatalf("expected missing key error")
	}
}
