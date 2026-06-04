package runtimeaudit

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestRunPassesWhenForbiddenCommandsAreAbsentAndFilesExist(t *testing.T) {
	dir := t.TempDir()
	required := filepath.Join(dir, "shkeeper")
	if err := os.WriteFile(required, []byte("binary"), 0o700); err != nil {
		t.Fatalf("write required file: %v", err)
	}
	result, err := Run([]string{"definitely-not-a-shkeeper-command"}, []string{required})
	if err != nil {
		t.Fatalf("Run() error = %v result=%+v", err, result)
	}
	if result.Status != "ok" || len(result.MissingFiles) != 0 || len(result.FoundCommands) != 0 {
		t.Fatalf("unexpected audit result: %+v", result)
	}
}

func TestRunFailsWhenForbiddenCommandExistsOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH executable permission semantics differ on Windows")
	}
	dir := t.TempDir()
	command := filepath.Join(dir, "forbidden-tool")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write command: %v", err)
	}
	t.Setenv("PATH", dir)
	result, err := Run([]string{"forbidden-tool"}, nil)
	if err == nil {
		t.Fatalf("expected audit failure, got result=%+v", result)
	}
	if result.Status != "fail" || result.FoundCommands["forbidden-tool"] == "" {
		t.Fatalf("forbidden command was not recorded: %+v", result)
	}
}

func TestRunFailsWhenRequiredFileIsMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	result, err := Run(nil, []string{missing})
	if err == nil {
		t.Fatalf("expected audit failure, got result=%+v", result)
	}
	if result.Status != "fail" || len(result.MissingFiles) != 1 || result.MissingFiles[0] != missing {
		t.Fatalf("missing file was not recorded: %+v", result)
	}
}

func TestDefaultRequiredFilesIncludeOperationalBinaries(t *testing.T) {
	required := DefaultRequiredFiles()
	for _, path := range []string{
		"/app/shkeeper",
		"/app/chain-worker",
		"/app/admin-account",
		"/app/worker-serverkey",
		"/app/deploy-check",
		"/app/final-plan",
		"/app/cutover-preflight",
		"/app/import-legacy-accounts",
		"/app/import-legacy-main-mariadb",
		"/app/import-legacy-json",
		"/app/cutover-audit",
		"/app/post-cutover-verify",
		"/app/release-audit",
		"/app/runtime-audit",
		"/app/goal-audit",
	} {
		if !slices.Contains(required, path) {
			t.Fatalf("default required files missing %s: %+v", path, required)
		}
	}
}
