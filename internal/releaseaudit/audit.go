package releaseaudit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/cutoveraudit"
	"github.com/Sky-JD/go-shkeeper/internal/deploycheck"
	"github.com/Sky-JD/go-shkeeper/internal/finalplan"
	"github.com/Sky-JD/go-shkeeper/internal/postcutover"
)

type Config struct {
	PlanFile               string
	DeployReportFiles      []string
	CutoverAuditFile       string
	PostCutoverFile        string
	OutputFile             string
	RequireMutating        bool
	RequireImportSync      bool
	RequirePreflight       bool
	RequireOrderMatrix     bool
	RequirePaymentCoverage bool
	RequirePayoutCoverage  bool
	RequirePaymentOrder    bool
	RequirePayoutOrder     bool
	RequirePayoutTxID      bool
	RequirePostCutover     bool
	RequireWorkerReady     bool
	RequireWorkerAddress   bool
}

type Report struct {
	Status             string            `json:"status"`
	GeneratedAt        time.Time         `json:"generated_at"`
	PlanFile           string            `json:"plan_file,omitempty"`
	DeployReportFiles  []string          `json:"deploy_report_files,omitempty"`
	CutoverAuditFile   string            `json:"cutover_audit_file,omitempty"`
	PostCutoverFile    string            `json:"post_cutover_file,omitempty"`
	CoverageCryptos    []string          `json:"coverage_cryptos,omitempty"`
	DeployStatuses     map[string]string `json:"deploy_statuses,omitempty"`
	CutoverAuditStatus string            `json:"cutover_audit_status,omitempty"`
	PostCutoverStatus  string            `json:"post_cutover_status,omitempty"`
	Gates              map[string]bool   `json:"gates"`
	Missing            []string          `json:"missing,omitempty"`
}

func LoadConfigFromEnv() Config {
	return Config{
		PlanFile:               strings.TrimSpace(os.Getenv("RELEASE_AUDIT_PLAN_FILE")),
		DeployReportFiles:      splitCSV(os.Getenv("RELEASE_AUDIT_DEPLOY_REPORT_FILES")),
		CutoverAuditFile:       strings.TrimSpace(os.Getenv("RELEASE_AUDIT_CUTOVER_AUDIT_FILE")),
		PostCutoverFile:        strings.TrimSpace(os.Getenv("RELEASE_AUDIT_POST_CUTOVER_FILE")),
		OutputFile:             strings.TrimSpace(os.Getenv("RELEASE_AUDIT_OUTPUT_FILE")),
		RequireMutating:        boolEnv("RELEASE_AUDIT_REQUIRE_MUTATING", true),
		RequireImportSync:      boolEnv("RELEASE_AUDIT_REQUIRE_IMPORT_SYNC", true),
		RequirePreflight:       boolEnv("RELEASE_AUDIT_REQUIRE_PREFLIGHT", true),
		RequireOrderMatrix:     boolEnv("RELEASE_AUDIT_REQUIRE_ORDER_STATUS_MATRIX", true),
		RequirePaymentCoverage: boolEnv("RELEASE_AUDIT_REQUIRE_PAYMENT_COVERAGE", true),
		RequirePayoutCoverage:  boolEnv("RELEASE_AUDIT_REQUIRE_PAYOUT_COVERAGE", true),
		RequirePaymentOrder:    boolEnv("RELEASE_AUDIT_REQUIRE_PAYMENT_ORDER", true),
		RequirePayoutOrder:     boolEnv("RELEASE_AUDIT_REQUIRE_PAYOUT_ORDER", true),
		RequirePayoutTxID:      boolEnv("RELEASE_AUDIT_REQUIRE_PAYOUT_TXID", true),
		RequirePostCutover:     boolEnv("RELEASE_AUDIT_REQUIRE_POST_CUTOVER", true),
		RequireWorkerReady:     boolEnv("RELEASE_AUDIT_REQUIRE_WORKER_READY", true),
		RequireWorkerAddress:   boolEnv("RELEASE_AUDIT_REQUIRE_WORKER_ADDRESS", true),
	}
}

func Run(cfg Config) (Report, error) {
	missing := make([]string, 0)
	gates := map[string]bool{
		"mutating":            cfg.RequireMutating,
		"import_sync":         cfg.RequireImportSync,
		"preflight":           cfg.RequirePreflight,
		"order_status_matrix": cfg.RequireOrderMatrix,
		"payment_coverage":    cfg.RequirePaymentCoverage,
		"payout_coverage":     cfg.RequirePayoutCoverage,
		"payment_order":       cfg.RequirePaymentOrder,
		"payout_order":        cfg.RequirePayoutOrder,
		"payout_txid":         cfg.RequirePayoutTxID,
		"post_cutover":        cfg.RequirePostCutover,
		"worker_ready":        cfg.RequireWorkerReady,
		"worker_address":      cfg.RequireWorkerAddress,
	}
	coverageCryptos := []string{}

	plan, planMissing := readPlan(cfg.PlanFile)
	missing = append(missing, planMissing...)
	if plan != nil {
		coverageCryptos = normalizeUpper(plan.CoverageCryptos)
		if len(coverageCryptos) == 0 {
			coverageCryptos = normalizeUpper(plan.Cryptos)
		}
		missing = append(missing, validatePlan(*plan, coverageCryptos, cfg)...)
	}

	deployStatuses, deployReports, deployMissing := readDeployReports(cfg.DeployReportFiles)
	missing = append(missing, deployMissing...)
	if len(coverageCryptos) == 0 {
		for _, report := range deployReports {
			if len(report.CoverageCryptos) > 0 {
				coverageCryptos = normalizeUpper(report.CoverageCryptos)
				break
			}
		}
	}
	if cfg.RequireMutating && len(deployReports) > 0 && !anyMutatingDeployReport(deployReports) {
		missing = append(missing, "deploy reports: no mutating final report was provided")
	}

	audit, auditMissing := readCutoverAudit(cfg.CutoverAuditFile)
	missing = append(missing, auditMissing...)
	cutoverStatus := ""
	if audit != nil {
		cutoverStatus = audit.Status
		missing = append(missing, validateCutoverAudit(*audit, coverageCryptos, cfg)...)
		if len(coverageCryptos) == 0 {
			coverageCryptos = normalizeUpper(audit.RequiredCryptos)
		}
	}

	postStatus := ""
	if cfg.RequirePostCutover {
		post, postMissing := readPostCutover(cfg.PostCutoverFile)
		missing = append(missing, postMissing...)
		if post != nil {
			postStatus = post.Status
			if post.Status != "pass" {
				missing = append(missing, "post-cutover report: status="+post.Status)
			}
			if len(post.Missing) > 0 {
				missing = append(missing, "post-cutover report: "+strings.Join(post.Missing, "; "))
			}
		}
	}

	sort.Strings(missing)
	status := "pass"
	if len(missing) > 0 {
		status = "fail"
	}
	return Report{
		Status:             status,
		GeneratedAt:        time.Now(),
		PlanFile:           cfg.PlanFile,
		DeployReportFiles:  append([]string(nil), cfg.DeployReportFiles...),
		CutoverAuditFile:   cfg.CutoverAuditFile,
		PostCutoverFile:    cfg.PostCutoverFile,
		CoverageCryptos:    coverageCryptos,
		DeployStatuses:     deployStatuses,
		CutoverAuditStatus: cutoverStatus,
		PostCutoverStatus:  postStatus,
		Gates:              gates,
		Missing:            missing,
	}, nil
}

func WriteReport(path string, report Report) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
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

func readPlan(path string) (*finalplan.Plan, []string) {
	if strings.TrimSpace(path) == "" {
		return nil, []string{"plan file: RELEASE_AUDIT_PLAN_FILE is required"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{"plan file " + path + ": " + err.Error()}
	}
	var plan finalplan.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, []string{"plan file " + path + ": " + err.Error()}
	}
	return &plan, nil
}

func readDeployReports(paths []string) (map[string]string, []deploycheck.Report, []string) {
	statuses := map[string]string{}
	reports := make([]deploycheck.Report, 0, len(paths))
	missing := make([]string, 0)
	if len(paths) == 0 {
		return statuses, reports, []string{"deploy reports: RELEASE_AUDIT_DEPLOY_REPORT_FILES is required"}
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
		reports = append(reports, report)
		statuses[path] = report.Status
		if report.Status != "ok" {
			reason := report.Error
			if reason == "" {
				reason = "status=" + report.Status
			}
			missing = append(missing, "deploy report "+path+": "+reason)
		}
	}
	return statuses, reports, missing
}

func readCutoverAudit(path string) (*cutoveraudit.Audit, []string) {
	if strings.TrimSpace(path) == "" {
		return nil, []string{"cutover audit: RELEASE_AUDIT_CUTOVER_AUDIT_FILE is required"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{"cutover audit " + path + ": " + err.Error()}
	}
	var audit cutoveraudit.Audit
	if err := json.Unmarshal(data, &audit); err != nil {
		return nil, []string{"cutover audit " + path + ": " + err.Error()}
	}
	return &audit, nil
}

func readPostCutover(path string) (*postcutover.Result, []string) {
	if strings.TrimSpace(path) == "" {
		return nil, []string{"post-cutover report: RELEASE_AUDIT_POST_CUTOVER_FILE is required"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{"post-cutover report " + path + ": " + err.Error()}
	}
	var result postcutover.Result
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, []string{"post-cutover report " + path + ": " + err.Error()}
	}
	return &result, nil
}

func validatePlan(plan finalplan.Plan, coverageCryptos []string, cfg Config) []string {
	missing := make([]string, 0)
	if len(coverageCryptos) == 0 {
		missing = append(missing, "plan: coverage_cryptos is empty")
	}
	if cfg.RequireMutating && !plan.Mutating {
		missing = append(missing, "plan: mutating must be true for final release")
	}
	if cfg.RequirePaymentCoverage && !plan.RequirePaymentCoverage {
		missing = append(missing, "plan: require_payment_coverage must be true")
	}
	if cfg.RequirePayoutCoverage && !plan.RequirePayoutCoverage {
		missing = append(missing, "plan: require_payout_coverage must be true")
	}
	if cfg.RequireOrderMatrix && !plan.OrderStatusMatrixCheck {
		missing = append(missing, "plan: order_status_matrix_check must be true")
	}
	if cfg.RequirePaymentCoverage {
		missing = append(missing, coverageMissing("plan payment_checks", coverageCryptos, paymentCryptos(plan.PaymentChecks))...)
	}
	if cfg.RequirePayoutCoverage {
		missing = append(missing, coverageMissing("plan payout_checks", coverageCryptos, payoutCryptos(plan.PayoutChecks))...)
	}
	if cfg.RequireWorkerAddress && len(plan.WorkerURLs) > 0 {
		workers := map[string]struct{}{}
		for _, check := range plan.WorkerAddressChecks {
			if strings.TrimSpace(check.Worker) != "" {
				workers[strings.ToLower(strings.TrimSpace(check.Worker))] = struct{}{}
			}
		}
		for worker := range plan.WorkerURLs {
			worker = strings.ToLower(strings.TrimSpace(worker))
			if worker == "" {
				continue
			}
			if _, ok := workers[worker]; !ok {
				missing = append(missing, "plan worker_address_checks: missing worker "+worker)
			}
		}
	}
	return missing
}

func validateCutoverAudit(audit cutoveraudit.Audit, coverageCryptos []string, cfg Config) []string {
	missing := make([]string, 0)
	if audit.Status != "pass" {
		reason := strings.Join(audit.Missing, "; ")
		if reason == "" {
			reason = "status=" + audit.Status
		}
		missing = append(missing, "cutover audit: "+reason)
	}
	if cfg.RequireImportSync && (!audit.Gates.ImportSync || !audit.ImportSync) {
		missing = append(missing, "cutover audit: import_sync gate was not proven")
	}
	if cfg.RequirePreflight && (!audit.Gates.Preflight || !audit.Preflight) {
		missing = append(missing, "cutover audit: preflight gate was not proven")
	}
	if cfg.RequireOrderMatrix && (!audit.Gates.OrderStatusMatrix || !audit.OrderMatrix) {
		missing = append(missing, "cutover audit: order_status_matrix gate was not proven")
	}
	if cfg.RequirePaymentOrder && !audit.Gates.PaymentOrder {
		missing = append(missing, "cutover audit: payment_order gate was not enabled")
	}
	if cfg.RequirePayoutOrder && !audit.Gates.PayoutOrder {
		missing = append(missing, "cutover audit: payout_order gate was not enabled")
	}
	if cfg.RequirePayoutTxID && !audit.Gates.PayoutTxID {
		missing = append(missing, "cutover audit: payout_txid gate was not enabled")
	}
	if cfg.RequireWorkerReady && !audit.Gates.WorkerReady {
		missing = append(missing, "cutover audit: worker_ready gate was not enabled")
	}
	if cfg.RequireWorkerAddress && !audit.Gates.WorkerAddress {
		missing = append(missing, "cutover audit: worker_address gate was not enabled")
	}
	required := coverageCryptos
	if len(required) == 0 {
		required = normalizeUpper(audit.RequiredCryptos)
	}
	for _, crypto := range required {
		row := audit.Coverage[crypto]
		if cfg.RequireOrderMatrix && !row.OrderStatusMatrix {
			missing = append(missing, "cutover audit: crypto "+crypto+" missing order_status_matrix")
		}
		if cfg.RequirePaymentOrder && !row.PaymentOrder {
			missing = append(missing, "cutover audit: crypto "+crypto+" missing payment_order")
		}
		if cfg.RequirePayoutOrder && !row.PayoutOrder {
			missing = append(missing, "cutover audit: crypto "+crypto+" missing payout_order")
		}
		if cfg.RequirePayoutTxID && !row.PayoutTxID {
			missing = append(missing, "cutover audit: crypto "+crypto+" missing payout txid")
		}
	}
	return missing
}

func anyMutatingDeployReport(reports []deploycheck.Report) bool {
	for _, report := range reports {
		if report.Mutating {
			return true
		}
	}
	return false
}

func paymentCryptos(checks []deploycheck.PaymentCheck) map[string]struct{} {
	out := map[string]struct{}{}
	for _, check := range checks {
		if crypto := strings.ToUpper(strings.TrimSpace(check.Crypto)); crypto != "" {
			out[crypto] = struct{}{}
		}
	}
	return out
}

func payoutCryptos(checks []deploycheck.PayoutCheck) map[string]struct{} {
	out := map[string]struct{}{}
	for _, check := range checks {
		if crypto := strings.ToUpper(strings.TrimSpace(check.Crypto)); crypto != "" {
			out[crypto] = struct{}{}
		}
	}
	return out
}

func coverageMissing(label string, required []string, covered map[string]struct{}) []string {
	missing := make([]string, 0)
	for _, crypto := range required {
		if _, ok := covered[crypto]; !ok {
			missing = append(missing, label+": missing "+crypto)
		}
	}
	return missing
}

func normalizeUpper(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "" {
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

func ErrorIfFailed(report Report) error {
	if report.Status == "pass" {
		return nil
	}
	return fmt.Errorf("release audit failed: %s", strings.Join(report.Missing, "; "))
}
