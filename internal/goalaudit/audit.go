package goalaudit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	StatusPass    = "pass"
	StatusBlocked = "blocked"
	StatusMissing = "missing"
	StatusFail    = "fail"
)

type Config struct {
	RuntimeAuditFile       string
	PreflightFile          string
	ReadinessFile          string
	DeployReportFiles      []string
	ReleaseAuditFile       string
	PostCutoverFile        string
	ContainerInventoryFile string
	ContainerStatsFile     string
	OutputFile             string
	ForbiddenContainers    []string
	ExpectedGoImage        string
	MaxContainerMemoryMB   int
	RequireStrictRelease   bool
}

type Report struct {
	Status       string        `json:"status"`
	GeneratedAt  time.Time     `json:"generated_at"`
	Requirements []Requirement `json:"requirements"`
	Missing      []string      `json:"missing,omitempty"`
}

type Requirement struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Evidence []string `json:"evidence,omitempty"`
	Missing  []string `json:"missing,omitempty"`
}

type runtimeAuditReport struct {
	Status            string            `json:"status"`
	ForbiddenCommands []string          `json:"forbidden_commands"`
	FoundCommands     map[string]string `json:"found_commands,omitempty"`
	RequiredFiles     []string          `json:"required_files,omitempty"`
	MissingFiles      []string          `json:"missing_files,omitempty"`
}

type preflightReport struct {
	Status             string            `json:"status"`
	DatabaseConfigured bool              `json:"database_url_configured"`
	MissingTables      []string          `json:"missing_tables,omitempty"`
	MissingIndexes     []string          `json:"missing_indexes,omitempty"`
	OrderIndexRows     int               `json:"order_index_rows"`
	OrderQueryPlans    []orderQueryPlan  `json:"order_query_plans,omitempty"`
	EnabledWalletCount int               `json:"enabled_wallet_count"`
	EnabledCryptos     []string          `json:"enabled_cryptos,omitempty"`
	Blockers           []string          `json:"blockers,omitempty"`
	ExplainWarnings    map[string]string `json:"explain_warnings,omitempty"`
}

type orderQueryPlan struct {
	Name string `json:"name"`
	Key  string `json:"key,omitempty"`
	Type string `json:"type,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type readinessReport struct {
	Status              string              `json:"status"`
	CoverageCryptos     []string            `json:"coverage_cryptos"`
	Workers             []string            `json:"workers"`
	PaymentCheckCount   int                 `json:"payment_check_count"`
	PayoutCheckCount    int                 `json:"payout_check_count"`
	WorkerAddressCount  int                 `json:"worker_address_count"`
	OrderStatusMatrix   bool                `json:"order_status_matrix_check"`
	PlaceholderGroups   map[string][]string `json:"placeholder_groups,omitempty"`
	MissingFieldGroups  map[string][]string `json:"missing_field_groups,omitempty"`
	ReadyForDeployCheck bool                `json:"ready_for_deploy_check"`
}

type deployReport struct {
	Status          string        `json:"status"`
	CoverageCryptos []string      `json:"coverage_cryptos,omitempty"`
	Mutating        bool          `json:"mutating"`
	Error           string        `json:"error,omitempty"`
	Checks          []reportCheck `json:"checks"`
}

type reportCheck struct {
	Name    string            `json:"name"`
	Status  string            `json:"status"`
	Details map[string]string `json:"details,omitempty"`
	Error   string            `json:"error,omitempty"`
}

type releaseAuditReport struct {
	Status          string          `json:"status"`
	CoverageCryptos []string        `json:"coverage_cryptos,omitempty"`
	Gates           map[string]bool `json:"gates,omitempty"`
	Missing         []string        `json:"missing,omitempty"`
}

type postCutoverReport struct {
	Status             string            `json:"status"`
	ObservedContainers map[string]string `json:"observed_containers,omitempty"`
	Missing            []string          `json:"missing,omitempty"`
}

type containerInventoryRow struct {
	Names string
	Name  string
	Image string
}

type containerStatsRow struct {
	Name      string
	Container string
	ID        string
	MemUsage  string
}

type containerMemoryStats struct {
	Name    string
	Raw     string
	UsedMiB float64
}

func LoadConfigFromEnv() Config {
	return Config{
		RuntimeAuditFile:       strings.TrimSpace(os.Getenv("GOAL_AUDIT_RUNTIME_AUDIT_FILE")),
		PreflightFile:          strings.TrimSpace(os.Getenv("GOAL_AUDIT_PREFLIGHT_FILE")),
		ReadinessFile:          strings.TrimSpace(os.Getenv("GOAL_AUDIT_READINESS_FILE")),
		DeployReportFiles:      splitCSV(os.Getenv("GOAL_AUDIT_DEPLOY_REPORT_FILES")),
		ReleaseAuditFile:       strings.TrimSpace(os.Getenv("GOAL_AUDIT_RELEASE_AUDIT_FILE")),
		PostCutoverFile:        strings.TrimSpace(os.Getenv("GOAL_AUDIT_POST_CUTOVER_FILE")),
		ContainerInventoryFile: strings.TrimSpace(os.Getenv("GOAL_AUDIT_CONTAINER_INVENTORY_FILE")),
		ContainerStatsFile:     strings.TrimSpace(os.Getenv("GOAL_AUDIT_CONTAINER_STATS_FILE")),
		OutputFile:             strings.TrimSpace(os.Getenv("GOAL_AUDIT_OUTPUT_FILE")),
		ForbiddenContainers:    splitCSV(env("GOAL_AUDIT_FORBIDDEN_CONTAINERS", "shkeeper,bnb-shkeeper,bnb_tasks,tron-shkeeper,tron_tasks")),
		ExpectedGoImage:        strings.TrimSpace(env("GOAL_AUDIT_EXPECTED_GO_IMAGE", "go-shkeeper")),
		MaxContainerMemoryMB:   intEnv("GOAL_AUDIT_MAX_CONTAINER_MEMORY_MB", 512),
		RequireStrictRelease:   boolEnv("GOAL_AUDIT_REQUIRE_STRICT_RELEASE_GATES", true),
	}
}

func Run(cfg Config) (Report, error) {
	runtimeReport, runtimeErr := readJSONFile[runtimeAuditReport](cfg.RuntimeAuditFile)
	preflight, preflightErr := readJSONFile[preflightReport](cfg.PreflightFile)
	readiness, readinessErr := readJSONFile[readinessReport](cfg.ReadinessFile)
	deployReports, deployMissing := readDeployReports(cfg.DeployReportFiles)
	release, releaseErr := readJSONFile[releaseAuditReport](cfg.ReleaseAuditFile)
	post, postErr := readJSONFile[postCutoverReport](cfg.PostCutoverFile)
	inventory, inventoryErr := readInventory(cfg.ContainerInventoryFile)
	containerStats, containerStatsErr := readContainerStats(cfg.ContainerStatsFile)

	requirements := []Requirement{
		auditRuntime(runtimeReport, runtimeErr),
		auditMariaDBPreflight(preflight, preflightErr),
		auditCompleteOrders(preflight, preflightErr, readiness, readinessErr, deployReports),
		auditAdminChange(readiness, readinessErr, deployReports),
		auditModularWorkers(readiness, readinessErr, deployReports),
		auditPerformance(preflight, preflightErr, deployReports, containerStats, containerStatsErr, cfg.MaxContainerMemoryMB),
		auditRelease(release, releaseErr, cfg.RequireStrictRelease),
		auditProductionCutover(cfg, post, postErr, inventory, inventoryErr),
	}
	status := StatusPass
	missing := make([]string, 0)
	for _, req := range requirements {
		if req.Status == StatusFail {
			status = StatusFail
		}
		if status != StatusFail && (req.Status == StatusBlocked || req.Status == StatusMissing) {
			status = StatusBlocked
		}
		for _, item := range req.Missing {
			missing = append(missing, req.Name+": "+item)
		}
	}
	for _, item := range deployMissing {
		missing = append(missing, "deploy_reports: "+item)
	}
	sort.Strings(missing)
	return Report{
		Status:       status,
		GeneratedAt:  time.Now().UTC(),
		Requirements: requirements,
		Missing:      missing,
	}, nil
}

func WriteReport(path string, report Report) error {
	path = strings.TrimSpace(path)
	if path == "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		_, err = os.Stdout.Write(data)
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
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

func auditRuntime(report runtimeAuditReport, err error) Requirement {
	req := Requirement{Name: "go_runtime_no_python_sqlite"}
	if err != nil {
		req.Status = StatusMissing
		req.Missing = []string{err.Error()}
		return req
	}
	req.Evidence = append(req.Evidence, "runtime_audit_status="+report.Status)
	req.Evidence = append(req.Evidence, "forbidden_commands="+strings.Join(report.ForbiddenCommands, ","))
	for _, path := range []string{"/app/shkeeper", "/app/chain-worker", "/app/admin-account", "/app/deploy-check", "/app/release-audit", "/app/runtime-audit", "/app/goal-audit"} {
		if !contains(report.RequiredFiles, path) {
			req.Missing = append(req.Missing, "required runtime file not audited: "+path)
		}
	}
	if report.Status != "ok" {
		req.Status = StatusFail
		req.Missing = append(req.Missing, "runtime audit status="+report.Status)
	}
	if len(report.FoundCommands) > 0 {
		req.Status = StatusFail
		req.Missing = append(req.Missing, "forbidden commands found")
	}
	if len(report.MissingFiles) > 0 {
		req.Status = StatusFail
		req.Missing = append(req.Missing, "missing runtime files: "+strings.Join(report.MissingFiles, ","))
	}
	if req.Status == "" {
		if len(req.Missing) > 0 {
			req.Status = StatusBlocked
		} else {
			req.Status = StatusPass
		}
	}
	return req
}

func auditMariaDBPreflight(report preflightReport, err error) Requirement {
	req := Requirement{Name: "mariadb_only_storage"}
	if err != nil {
		req.Status = StatusMissing
		req.Missing = []string{err.Error()}
		return req
	}
	req.Evidence = append(req.Evidence, "preflight_status="+report.Status)
	req.Evidence = append(req.Evidence, fmt.Sprintf("enabled_wallets=%d", report.EnabledWalletCount))
	if len(report.EnabledCryptos) > 0 {
		req.Evidence = append(req.Evidence, "enabled_cryptos="+strings.Join(report.EnabledCryptos, ","))
	}
	if report.Status != "pass" {
		req.Status = StatusFail
		req.Missing = append(req.Missing, "preflight status="+report.Status)
	}
	if !report.DatabaseConfigured {
		req.Missing = append(req.Missing, "MariaDB database URL was not configured")
	}
	if len(report.MissingTables) > 0 {
		req.Missing = append(req.Missing, "missing tables: "+strings.Join(report.MissingTables, ","))
	}
	if len(report.MissingIndexes) > 0 {
		req.Missing = append(req.Missing, "missing indexes: "+strings.Join(report.MissingIndexes, ","))
	}
	if len(report.Blockers) > 0 {
		req.Missing = append(req.Missing, "preflight blockers: "+strings.Join(report.Blockers, "; "))
	}
	if req.Status == "" {
		if len(req.Missing) > 0 {
			req.Status = StatusBlocked
		} else {
			req.Status = StatusPass
		}
	}
	return req
}

func auditCompleteOrders(preflight preflightReport, preflightErr error, readiness readinessReport, readinessErr error, deployReports []deployReport) Requirement {
	req := Requirement{Name: "complete_order_query"}
	if preflightErr != nil {
		req.Missing = append(req.Missing, "preflight: "+preflightErr.Error())
	} else {
		req.Evidence = append(req.Evidence, fmt.Sprintf("order_index_rows=%d", preflight.OrderIndexRows))
		req.Evidence = append(req.Evidence, fmt.Sprintf("order_query_plans=%d", len(preflight.OrderQueryPlans)))
		if preflight.OrderIndexRows <= 0 {
			req.Missing = append(req.Missing, "order_index has no rows")
		}
		if len(preflight.OrderQueryPlans) == 0 {
			req.Missing = append(req.Missing, "missing order query EXPLAIN plans")
		}
	}
	if readinessErr != nil {
		req.Missing = append(req.Missing, "readiness: "+readinessErr.Error())
	} else {
		req.Evidence = append(req.Evidence, fmt.Sprintf("order_status_matrix_check=%t", readiness.OrderStatusMatrix))
		req.Evidence = append(req.Evidence, "coverage_cryptos="+strings.Join(readiness.CoverageCryptos, ","))
		if !readiness.OrderStatusMatrix {
			req.Missing = append(req.Missing, "final plan does not enable order_status_matrix_check")
		}
	}
	if hasCheck(deployReports, "order_status_matrix") {
		req.Evidence = append(req.Evidence, "deploy_check_order_status_matrix=ok")
	} else {
		req.Missing = append(req.Missing, "final deploy-check has not proven order_status_matrix")
	}
	if len(req.Missing) == 0 {
		req.Status = StatusPass
	} else if preflightErr != nil && readinessErr != nil {
		req.Status = StatusMissing
	} else {
		req.Status = StatusBlocked
	}
	return req
}

func auditAdminChange(readiness readinessReport, readinessErr error, deployReports []deployReport) Requirement {
	req := Requirement{Name: "admin_account_password_change"}
	if hasCheck(deployReports, "admin_account_roundtrip") {
		req.Status = StatusPass
		req.Evidence = []string{"deploy_check_admin_account_roundtrip=ok"}
		return req
	}
	if readinessErr != nil {
		req.Status = StatusMissing
		req.Missing = []string{"readiness: " + readinessErr.Error()}
		return req
	}
	req.Evidence = append(req.Evidence, "final_plan_mutating_admin_check_configured=true")
	if len(readiness.PlaceholderGroups["admin_credentials"]) > 0 || len(readiness.MissingFieldGroups["admin_credentials"]) > 0 {
		req.Status = StatusBlocked
		req.Missing = append(req.Missing, "admin credential placeholders remain in final readiness")
		return req
	}
	req.Status = StatusBlocked
	req.Missing = append(req.Missing, "final deploy-check has not proven admin_account_roundtrip")
	return req
}

func auditModularWorkers(readiness readinessReport, readinessErr error, deployReports []deployReport) Requirement {
	req := Requirement{Name: "modular_chain_workers"}
	if readinessErr != nil {
		req.Status = StatusMissing
		req.Missing = []string{"readiness: " + readinessErr.Error()}
		return req
	}
	req.Evidence = append(req.Evidence, "workers="+strings.Join(readiness.Workers, ","))
	req.Evidence = append(req.Evidence, fmt.Sprintf("worker_address_checks=%d", readiness.WorkerAddressCount))
	if len(readiness.Workers) == 0 {
		req.Missing = append(req.Missing, "no workers in final readiness")
	}
	if readiness.WorkerAddressCount < len(readiness.Workers) {
		req.Missing = append(req.Missing, "worker address proof count is lower than worker count")
	}
	if len(readiness.PlaceholderGroups["worker_credentials"]) > 0 || len(readiness.MissingFieldGroups["worker_credentials"]) > 0 {
		req.Missing = append(req.Missing, "worker credential placeholders remain")
	}
	if hasCheck(deployReports, "worker_address") {
		req.Evidence = append(req.Evidence, "deploy_check_worker_address=ok")
	} else {
		req.Missing = append(req.Missing, "final deploy-check has not proven worker_address")
	}
	if len(req.Missing) == 0 {
		req.Status = StatusPass
	} else {
		req.Status = StatusBlocked
	}
	return req
}

func auditPerformance(preflight preflightReport, preflightErr error, deployReports []deployReport, containerStats []containerMemoryStats, containerStatsErr error, maxMemoryMB int) Requirement {
	req := Requirement{Name: "high_concurrency_fast_api"}
	if preflightErr != nil {
		req.Status = StatusMissing
		req.Missing = []string{"preflight: " + preflightErr.Error()}
		return req
	}
	req.Evidence = append(req.Evidence, fmt.Sprintf("order_query_plans=%d", len(preflight.OrderQueryPlans)))
	for _, plan := range preflight.OrderQueryPlans {
		if strings.TrimSpace(plan.Key) == "" {
			req.Missing = append(req.Missing, "query plan "+plan.Name+" did not select an index")
		}
	}
	if len(preflight.ExplainWarnings) > 0 {
		req.Missing = append(req.Missing, "EXPLAIN warnings present")
	}
	if hasCheck(deployReports, "order_lookup_parallel") {
		req.Evidence = append(req.Evidence, "deploy_check_order_lookup_parallel=ok")
	} else {
		req.Missing = append(req.Missing, "missing final order_lookup_parallel deploy-check")
	}
	if hasCheck(deployReports, "order_list_parallel") {
		req.Evidence = append(req.Evidence, "deploy_check_order_list_parallel=ok")
	} else {
		req.Missing = append(req.Missing, "missing final order_list_parallel deploy-check")
	}
	if containerStatsErr != nil {
		req.Missing = append(req.Missing, "container stats: "+containerStatsErr.Error())
	} else if len(containerStats) == 0 {
		req.Missing = append(req.Missing, "container stats: no rows")
	} else {
		var maxSeen containerMemoryStats
		for i, stat := range containerStats {
			if i == 0 || stat.UsedMiB > maxSeen.UsedMiB {
				maxSeen = stat
			}
			if maxMemoryMB > 0 && stat.UsedMiB > float64(maxMemoryMB) {
				req.Missing = append(req.Missing, fmt.Sprintf("container %s memory %.1fMiB exceeds limit %dMiB", stat.Name, stat.UsedMiB, maxMemoryMB))
			}
		}
		req.Evidence = append(req.Evidence, fmt.Sprintf("container_stats=%d", len(containerStats)))
		req.Evidence = append(req.Evidence, fmt.Sprintf("container_memory_max_mib=%.1f", maxSeen.UsedMiB))
		if maxMemoryMB > 0 {
			req.Evidence = append(req.Evidence, fmt.Sprintf("container_memory_limit_mib=%d", maxMemoryMB))
		}
	}
	if len(req.Missing) == 0 {
		req.Status = StatusPass
	} else {
		req.Status = StatusBlocked
	}
	return req
}

func auditRelease(report releaseAuditReport, err error, requireStrict bool) Requirement {
	req := Requirement{Name: "strict_release_audit"}
	if err != nil {
		req.Status = StatusMissing
		req.Missing = []string{err.Error()}
		return req
	}
	req.Evidence = append(req.Evidence, "release_audit_status="+report.Status)
	if len(report.CoverageCryptos) > 0 {
		req.Evidence = append(req.Evidence, "coverage_cryptos="+strings.Join(report.CoverageCryptos, ","))
	}
	if len(report.Gates) > 0 {
		req.Evidence = append(req.Evidence, "release_gates="+enabledGateList(report.Gates))
	}
	if report.Status != "pass" {
		req.Status = StatusFail
		req.Missing = append(req.Missing, report.Missing...)
		if len(req.Missing) == 0 {
			req.Missing = append(req.Missing, "release audit status="+report.Status)
		}
		return req
	}
	if requireStrict {
		for _, gate := range requiredReleaseGates() {
			if !report.Gates[gate] {
				req.Missing = append(req.Missing, "release audit gate not strict: "+gate)
			}
		}
	}
	if len(req.Missing) > 0 {
		req.Status = StatusFail
		return req
	}
	req.Status = StatusPass
	return req
}

func requiredReleaseGates() []string {
	return []string{
		"mutating",
		"import_sync",
		"preflight",
		"order_status_matrix",
		"payment_coverage",
		"payout_coverage",
		"payment_order",
		"payout_order",
		"payout_txid",
		"post_cutover",
		"worker_ready",
		"worker_address",
	}
}

func enabledGateList(gates map[string]bool) string {
	enabled := make([]string, 0, len(gates))
	for key, value := range gates {
		if value {
			enabled = append(enabled, key)
		}
	}
	sort.Strings(enabled)
	return strings.Join(enabled, ",")
}

func auditProductionCutover(cfg Config, post postCutoverReport, postErr error, inventory map[string]string, inventoryErr error) Requirement {
	req := Requirement{Name: "production_go_cutover"}
	if postErr == nil {
		req.Evidence = append(req.Evidence, "post_cutover_status="+post.Status)
		if post.Status != "pass" {
			req.Missing = append(req.Missing, post.Missing...)
			if len(req.Missing) == 0 {
				req.Missing = append(req.Missing, "post-cutover status="+post.Status)
			}
		}
	} else {
		req.Missing = append(req.Missing, "post-cutover: "+postErr.Error())
	}
	if inventoryErr == nil {
		req.Evidence = append(req.Evidence, fmt.Sprintf("observed_containers=%d", len(inventory)))
		for _, name := range cfg.ForbiddenContainers {
			if image := inventory[name]; image != "" {
				req.Missing = append(req.Missing, "legacy container still running: "+name+" image="+image)
			}
		}
		if cfg.ExpectedGoImage != "" {
			found := false
			for _, image := range inventory {
				if strings.Contains(image, cfg.ExpectedGoImage) {
					found = true
					break
				}
			}
			if !found {
				req.Missing = append(req.Missing, "no running container image contains "+cfg.ExpectedGoImage)
			}
		}
	} else {
		req.Missing = append(req.Missing, "container inventory: "+inventoryErr.Error())
	}
	if len(req.Missing) == 0 {
		req.Status = StatusPass
	} else {
		req.Status = StatusBlocked
	}
	return req
}

func readDeployReports(paths []string) ([]deployReport, []string) {
	reports := make([]deployReport, 0, len(paths))
	missing := make([]string, 0)
	for _, path := range paths {
		report, err := readJSONFile[deployReport](path)
		if err != nil {
			missing = append(missing, err.Error())
			continue
		}
		reports = append(reports, report)
	}
	return reports, missing
}

func hasCheck(reports []deployReport, name string) bool {
	for _, report := range reports {
		if report.Status != "ok" {
			continue
		}
		for _, check := range report.Checks {
			if check.Name == name && check.Status == "ok" {
				return true
			}
		}
	}
	return false
}

func readJSONFile[T any](path string) (T, error) {
	var out T
	path = strings.TrimSpace(path)
	if path == "" {
		return out, fmt.Errorf("file path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("decode %s: %w", path, err)
	}
	return out, nil
}

func readInventory(path string) (map[string]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer file.Close()
	out := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row containerInventoryRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("decode inventory row: %w", err)
		}
		name := strings.TrimSpace(row.Names)
		if name == "" {
			name = strings.TrimSpace(row.Name)
		}
		if name != "" {
			out[name] = strings.TrimSpace(row.Image)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func readContainerStats(path string) ([]containerMemoryStats, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer file.Close()
	out := make([]containerMemoryStats, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row containerStatsRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("decode container stats row: %w", err)
		}
		name := strings.TrimSpace(row.Name)
		if name == "" {
			name = strings.TrimSpace(row.Container)
		}
		if name == "" {
			name = strings.TrimSpace(row.ID)
		}
		if name == "" {
			name = "container"
		}
		used, err := parseMemoryMiB(row.MemUsage)
		if err != nil {
			return nil, fmt.Errorf("container stats %s: %w", name, err)
		}
		out = append(out, containerMemoryStats{Name: name, Raw: row.MemUsage, UsedMiB: used})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func parseMemoryMiB(value string) (float64, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return 0, fmt.Errorf("MemUsage is empty")
	}
	used := raw
	if before, _, ok := strings.Cut(raw, "/"); ok {
		used = before
	}
	used = strings.TrimSpace(strings.ReplaceAll(used, ",", ""))
	if used == "" {
		return 0, fmt.Errorf("MemUsage %q has no used value", value)
	}
	numberEnd := 0
	for numberEnd < len(used) {
		ch := used[numberEnd]
		if (ch >= '0' && ch <= '9') || ch == '.' {
			numberEnd++
			continue
		}
		break
	}
	if numberEnd == 0 {
		return 0, fmt.Errorf("MemUsage %q has no numeric used value", value)
	}
	number, err := strconv.ParseFloat(used[:numberEnd], 64)
	if err != nil {
		return 0, fmt.Errorf("parse MemUsage %q: %w", value, err)
	}
	unit := strings.ToLower(strings.TrimSpace(used[numberEnd:]))
	switch unit {
	case "", "b":
		return number / 1024 / 1024, nil
	case "kib":
		return number / 1024, nil
	case "kb":
		return number * 1000 / 1024 / 1024, nil
	case "mib":
		return number, nil
	case "mb":
		return number * 1000 * 1000 / 1024 / 1024, nil
	case "gib":
		return number * 1024, nil
	case "gb":
		return number * 1000 * 1000 * 1000 / 1024 / 1024, nil
	default:
		return 0, fmt.Errorf("unsupported MemUsage unit %q", unit)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func boolEnv(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
