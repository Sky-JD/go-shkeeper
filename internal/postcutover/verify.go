package postcutover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/cutoveraudit"
	"github.com/Sky-JD/go-shkeeper/internal/deploycheck"
)

type Config struct {
	MainURL                  string
	WorkerURLs               []NamedURL
	DeployReportFiles        []string
	CutoverAuditFile         string
	ContainerInventoryFile   string
	OutputFile               string
	ExpectedContainers       []ContainerExpectation
	ForbiddenContainers      []string
	ExpectedContainerImages  []string
	ForbiddenContainerImages []string
	RequireAuditOrderMatrix  bool
	RequireAuditPayoutTxID   bool
	Timeout                  time.Duration
}

type NamedURL struct {
	Name string
	URL  string
}

type ContainerExpectation struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

type Result struct {
	Status              string                 `json:"status"`
	GeneratedAt         time.Time              `json:"generated_at"`
	MainURL             string                 `json:"main_url,omitempty"`
	Workers             map[string]bool        `json:"workers,omitempty"`
	DeployReports       map[string]string      `json:"deploy_reports"`
	CutoverAuditStatus  string                 `json:"cutover_audit_status,omitempty"`
	ExpectedContainers  []ContainerExpectation `json:"expected_containers,omitempty"`
	ForbiddenContainers []string               `json:"forbidden_containers,omitempty"`
	ExpectedImages      []string               `json:"expected_images,omitempty"`
	ForbiddenImages     []string               `json:"forbidden_images,omitempty"`
	ObservedContainers  map[string]string      `json:"observed_containers,omitempty"`
	ObservedImages      []string               `json:"observed_images,omitempty"`
	Missing             []string               `json:"missing,omitempty"`
}

type containerRow struct {
	Names string
	Name  string
	Image string
}

func LoadConfigFromEnv() Config {
	return Config{
		MainURL:                  strings.TrimSpace(os.Getenv("POST_CUTOVER_MAIN_URL")),
		WorkerURLs:               parseNamedURLs(os.Getenv("POST_CUTOVER_WORKER_URLS")),
		DeployReportFiles:        splitCSV(os.Getenv("POST_CUTOVER_DEPLOY_REPORT_FILES")),
		CutoverAuditFile:         strings.TrimSpace(os.Getenv("POST_CUTOVER_AUDIT_FILE")),
		ContainerInventoryFile:   strings.TrimSpace(os.Getenv("POST_CUTOVER_CONTAINER_INVENTORY_FILE")),
		OutputFile:               strings.TrimSpace(os.Getenv("POST_CUTOVER_OUTPUT_FILE")),
		ExpectedContainers:       parseContainerExpectations(os.Getenv("POST_CUTOVER_EXPECTED_CONTAINERS")),
		ForbiddenContainers:      normalizeNames(splitCSV(os.Getenv("POST_CUTOVER_FORBIDDEN_CONTAINERS"))),
		ExpectedContainerImages:  splitCSV(os.Getenv("POST_CUTOVER_EXPECTED_IMAGES")),
		ForbiddenContainerImages: splitCSV(os.Getenv("POST_CUTOVER_FORBIDDEN_IMAGES")),
		RequireAuditOrderMatrix:  boolEnv("POST_CUTOVER_REQUIRE_AUDIT_ORDER_STATUS_MATRIX", true),
		RequireAuditPayoutTxID:   boolEnv("POST_CUTOVER_REQUIRE_AUDIT_PAYOUT_TXID", false),
		Timeout:                  secondsEnv("POST_CUTOVER_TIMEOUT_SECONDS", 10),
	}
}

func Verify(ctx context.Context, cfg Config) (Result, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	client := &http.Client{Timeout: cfg.Timeout}
	missing := make([]string, 0)
	workers := map[string]bool{}
	if cfg.MainURL != "" {
		if err := checkReady(ctx, client, cfg.MainURL, "/healthz", "ok"); err != nil {
			missing = append(missing, "main healthz: "+err.Error())
		}
		if err := checkReady(ctx, client, cfg.MainURL, "/readyz", "ready"); err != nil {
			missing = append(missing, "main readyz: "+err.Error())
		}
	} else {
		missing = append(missing, "main url: POST_CUTOVER_MAIN_URL is required")
	}
	if len(cfg.WorkerURLs) == 0 {
		missing = append(missing, "workers: POST_CUTOVER_WORKER_URLS is required")
	}
	for _, worker := range cfg.WorkerURLs {
		if worker.Name == "" {
			worker.Name = "worker"
		}
		ok := true
		if err := checkReady(ctx, client, worker.URL, "/readyz", "ready"); err != nil {
			ok = false
			missing = append(missing, "worker "+worker.Name+" readyz: "+err.Error())
		}
		workers[worker.Name] = ok
	}
	reportStatuses, reportMissing := verifyDeployReports(cfg.DeployReportFiles)
	missing = append(missing, reportMissing...)
	auditStatus, auditMissing := verifyCutoverAudit(cfg)
	missing = append(missing, auditMissing...)
	observedContainers, observedImages, inventoryMissing, err := verifyContainerInventory(cfg)
	if err != nil {
		missing = append(missing, err.Error())
	} else {
		missing = append(missing, inventoryMissing...)
	}
	status := "pass"
	if len(missing) > 0 {
		status = "fail"
	}
	result := Result{
		Status:              status,
		GeneratedAt:         time.Now(),
		MainURL:             cfg.MainURL,
		Workers:             workers,
		DeployReports:       reportStatuses,
		CutoverAuditStatus:  auditStatus,
		ExpectedContainers:  append([]ContainerExpectation(nil), cfg.ExpectedContainers...),
		ForbiddenContainers: append([]string(nil), cfg.ForbiddenContainers...),
		ExpectedImages:      append([]string(nil), cfg.ExpectedContainerImages...),
		ForbiddenImages:     append([]string(nil), cfg.ForbiddenContainerImages...),
		ObservedContainers:  observedContainers,
		ObservedImages:      observedImages,
		Missing:             missing,
	}
	return result, nil
}

func WriteResult(path string, result Result) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create post-cutover output dir %s: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal post-cutover result: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func checkReady(ctx context.Context, client *http.Client, baseURL, path, want string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/"+strings.TrimLeft(path, "/"), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	got := strings.TrimSpace(fmt.Sprint(payload["status"]))
	if want != "" && got != want {
		return fmt.Errorf("expected status %q, got %q", want, got)
	}
	return nil
}

func verifyDeployReports(paths []string) (map[string]string, []string) {
	statuses := map[string]string{}
	missing := make([]string, 0)
	if len(paths) == 0 {
		return statuses, []string{"deploy reports: POST_CUTOVER_DEPLOY_REPORT_FILES is required"}
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			missing = append(missing, "deploy report "+path+": "+err.Error())
			continue
		}
		var report deploycheck.Report
		if err := json.Unmarshal(data, &report); err != nil {
			missing = append(missing, "deploy report "+path+": "+err.Error())
			continue
		}
		statuses[path] = report.Status
		if report.Status != "ok" {
			reason := report.Error
			if reason == "" {
				reason = "status=" + report.Status
			}
			missing = append(missing, "deploy report "+path+": "+reason)
		}
	}
	return statuses, missing
}

func verifyCutoverAudit(cfg Config) (string, []string) {
	path := cfg.CutoverAuditFile
	if strings.TrimSpace(path) == "" {
		return "", []string{"cutover audit: POST_CUTOVER_AUDIT_FILE is required"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", []string{"cutover audit " + path + ": " + err.Error()}
	}
	var audit cutoveraudit.Audit
	if err := json.Unmarshal(data, &audit); err != nil {
		return "", []string{"cutover audit " + path + ": " + err.Error()}
	}
	if audit.Status != "pass" {
		reasons := strings.Join(audit.Missing, "; ")
		if reasons == "" {
			reasons = "status=" + audit.Status
		}
		return audit.Status, []string{"cutover audit " + path + ": " + reasons}
	}
	missing := make([]string, 0)
	if cfg.RequireAuditOrderMatrix && (!audit.Gates.OrderStatusMatrix || !audit.OrderMatrix) {
		missing = append(missing, "cutover audit "+path+": order_status_matrix gate was not proven")
	}
	if cfg.RequireAuditPayoutTxID {
		if !audit.Gates.PayoutTxID {
			missing = append(missing, "cutover audit "+path+": payout txid gate was not enabled")
		}
		for crypto, coverage := range audit.Coverage {
			if !coverage.PayoutTxID {
				missing = append(missing, "cutover audit "+path+": crypto "+crypto+" missing payout txid")
			}
		}
	}
	return audit.Status, missing
}

func verifyContainerInventory(cfg Config) (map[string]string, []string, []string, error) {
	if cfg.ContainerInventoryFile == "" &&
		len(cfg.ExpectedContainers) == 0 &&
		len(cfg.ForbiddenContainers) == 0 &&
		len(cfg.ExpectedContainerImages) == 0 &&
		len(cfg.ForbiddenContainerImages) == 0 {
		return nil, nil, nil, nil
	}
	if cfg.ContainerInventoryFile == "" {
		return nil, nil, nil, errors.New("container inventory: POST_CUTOVER_CONTAINER_INVENTORY_FILE is required when container or image checks are configured")
	}
	rows, err := readInventory(cfg.ContainerInventoryFile)
	if err != nil {
		return nil, nil, nil, err
	}
	observed := make([]string, 0, len(rows))
	containers := map[string]string{}
	for _, row := range rows {
		if row.Image != "" {
			observed = append(observed, row.Image)
		}
		for _, name := range row.containerNames() {
			containers[name] = row.Image
		}
	}
	sort.Strings(observed)
	missing := make([]string, 0)
	for _, expected := range cfg.ExpectedContainers {
		name := normalizeName(expected.Name)
		if name == "" {
			continue
		}
		image, ok := containers[name]
		if !ok {
			missing = append(missing, "expected container missing: "+name)
			continue
		}
		if strings.TrimSpace(expected.Image) != "" && !containsStringFold(image, expected.Image) {
			missing = append(missing, fmt.Sprintf("expected container image mismatch: %s expected image containing %q got %q", name, expected.Image, image))
		}
	}
	for _, forbidden := range cfg.ForbiddenContainers {
		name := normalizeName(forbidden)
		if name == "" {
			continue
		}
		if _, ok := containers[name]; ok {
			missing = append(missing, "forbidden container still running: "+name)
		}
	}
	for _, expected := range cfg.ExpectedContainerImages {
		if !containsImage(observed, expected) {
			missing = append(missing, "expected image missing: "+expected)
		}
	}
	for _, forbidden := range cfg.ForbiddenContainerImages {
		if containsImage(observed, forbidden) {
			missing = append(missing, "forbidden image still running: "+forbidden)
		}
	}
	return containers, observed, missing, nil
}

func readInventory(path string) ([]containerRow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("container inventory %s: %w", path, err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	rows := make([]containerRow, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var row containerRow
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("container inventory %s: %w", path, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (row containerRow) containerNames() []string {
	raw := row.Names
	if raw == "" {
		raw = row.Name
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if name := normalizeName(part); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func containsImage(observed []string, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return true
	}
	for _, image := range observed {
		if containsStringFold(image, pattern) {
			return true
		}
	}
	return false
}

func containsStringFold(value string, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return true
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(value)), pattern)
}

func parseNamedURLs(value string) []NamedURL {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]NamedURL, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := fmt.Sprintf("worker%d", len(out)+1)
		urlValue := part
		if before, after, ok := strings.Cut(part, "="); ok {
			name = strings.TrimSpace(before)
			urlValue = strings.TrimSpace(after)
		}
		if urlValue != "" {
			out = append(out, NamedURL{Name: name, URL: urlValue})
		}
	}
	return out
}

func parseContainerExpectations(value string) []ContainerExpectation {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]ContainerExpectation, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		image := ""
		if before, after, ok := strings.Cut(part, "="); ok {
			name = before
			image = after
		}
		name = normalizeName(name)
		image = strings.TrimSpace(image)
		if name != "" {
			out = append(out, ContainerExpectation{Name: name, Image: image})
		}
	}
	return out
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
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

func normalizeNames(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if name := normalizeName(value); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func normalizeName(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "/")
}

func secondsEnv(key string, fallback int) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return time.Duration(fallback) * time.Second
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil || value <= 0 {
		value = fallback
	}
	return time.Duration(value) * time.Second
}

func boolEnv(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
