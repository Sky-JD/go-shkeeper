package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReportDoesNotIncludePassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-account.json")
	rep := report{
		Status:          "ok",
		UpdatedAt:       "2026-06-04T12:00:00Z",
		UserID:          7,
		Username:        "cutover-admin",
		PasswordUpdated: true,
	}
	if err := writeReport(path, rep); err != nil {
		t.Fatalf("writeReport() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"status": "ok"`, `"username": "cutover-admin"`, `"password_updated": true`} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %s: %s", want, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "password\":") || strings.Contains(text, "secret") {
		t.Fatalf("report should not include password material: %s", text)
	}
}
