package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerKeyFromEnvUsesSharedCredentials(t *testing.T) {
	t.Setenv("WORKER_USERNAME", "worker")
	t.Setenv("WORKER_PASSWORD", "worker-pass")
	key, username, err := serverKeyFromEnv()
	if err != nil {
		t.Fatalf("serverKeyFromEnv() error = %v", err)
	}
	if key != "worker:worker-pass" || username != "worker" {
		t.Fatalf("unexpected key or username: key=%q username=%q", key, username)
	}
}

func TestServerKeyFromEnvSupportsSecretFile(t *testing.T) {
	dir := t.TempDir()
	passwordFile := filepath.Join(dir, "worker-password")
	if err := os.WriteFile(passwordFile, []byte("file-pass\n"), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}
	t.Setenv("WORKER_USERNAME", "worker")
	t.Setenv("WORKER_PASSWORD_FILE", passwordFile)
	key, username, err := serverKeyFromEnv()
	if err != nil {
		t.Fatalf("serverKeyFromEnv() error = %v", err)
	}
	if key != "worker:file-pass" || username != "worker" {
		t.Fatalf("unexpected file-backed key: key=%q username=%q", key, username)
	}
}

func TestWriteReportDoesNotIncludePassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-serverkey.json")
	rep := report{
		Status:           "ok",
		UpdatedAt:        "2026-06-04T12:00:00Z",
		Cryptos:          []string{"BNB-USDT", "TRX"},
		Username:         "worker",
		UpdatedCount:     2,
		CredentialsSaved: true,
	}
	if err := writeReport(path, rep); err != nil {
		t.Fatalf("writeReport() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"status": "ok"`, `"username": "worker"`, `"updated_count": 2`} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %s: %s", want, text)
		}
	}
	for _, forbidden := range []string{"worker-pass", "file-pass", "serverkey", "password"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("report should not include secret material %q: %s", forbidden, text)
		}
	}
}

func TestSplitCSVDedupesAndNormalizesCryptos(t *testing.T) {
	got := strings.Join(splitCSV(" bnb-usdt,TRX,bnb-usdt, usdc "), ",")
	if got != "BNB-USDT,TRX,USDC" {
		t.Fatalf("unexpected splitCSV result: %s", got)
	}
}
