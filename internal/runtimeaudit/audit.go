package runtimeaudit

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Result struct {
	Status            string            `json:"status"`
	ForbiddenCommands []string          `json:"forbidden_commands"`
	FoundCommands     map[string]string `json:"found_commands,omitempty"`
	RequiredFiles     []string          `json:"required_files,omitempty"`
	MissingFiles      []string          `json:"missing_files,omitempty"`
}

func Run(forbidden []string, requiredFiles []string) (Result, error) {
	forbidden = normalizeList(forbidden)
	requiredFiles = normalizeList(requiredFiles)
	found := map[string]string{}
	for _, name := range forbidden {
		if path, err := exec.LookPath(name); err == nil && strings.TrimSpace(path) != "" {
			found[name] = path
		}
	}
	missing := make([]string, 0)
	for _, path := range requiredFiles {
		if _, err := os.Stat(path); err != nil {
			missing = append(missing, path)
		}
	}
	status := "ok"
	var err error
	if len(found) > 0 || len(missing) > 0 {
		status = "fail"
		err = errors.New("runtime audit failed")
	}
	result := Result{
		Status:            status,
		ForbiddenCommands: forbidden,
		RequiredFiles:     requiredFiles,
		MissingFiles:      missing,
	}
	if len(found) > 0 {
		result.FoundCommands = found
	}
	return result, err
}

func DefaultForbiddenCommands() []string {
	return []string{"python", "python3", "pip", "pip3", "sqlite3"}
}

func DefaultRequiredFiles() []string {
	return []string{
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
	}
}

func CommandsFromEnv() []string {
	if raw := strings.TrimSpace(os.Getenv("RUNTIME_AUDIT_FORBIDDEN_COMMANDS")); raw != "" {
		return splitCSV(raw)
	}
	return DefaultForbiddenCommands()
}

func RequiredFilesFromEnv() []string {
	if raw := strings.TrimSpace(os.Getenv("RUNTIME_AUDIT_REQUIRED_FILES")); raw != "" {
		return splitCSV(raw)
	}
	return DefaultRequiredFiles()
}

func Marshal(result Result) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func normalizeList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(filepath.Clean(value))
		if value == "." || value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
