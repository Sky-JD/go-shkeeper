package finalplan

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildPlanFromExplicitCryptos(t *testing.T) {
	cfg := Config{
		MainURL:               "http://go-shkeeper:5000",
		Cryptos:               []string{"bnb-usdt", "TRX", "USDT", "USDC"},
		UseWalletCryptos:      false,
		APIKey:                "api-key",
		AdminUsername:         "admin",
		AdminPassword:         "admin-pass",
		AdminUpdateUsername:   "admin2",
		AdminUpdatePassword:   "admin2-pass",
		PaymentAmount:         "1",
		PayoutAmount:          "0.01",
		ReportFile:            "/reports/final.json",
		ExpectServerStatus:    "Synced",
		OrderRequests:         10,
		OrderConcurrency:      2,
		OrderMaxLatencyMS:     500,
		OrderListRequests:     10,
		OrderListConcurrency:  2,
		OrderListMaxLatencyMS: 500,
		Requests:              10,
		Concurrency:           2,
		TimeoutSeconds:        30,
		Mutating:              true,
		RequirePayment:        true,
		RequirePayout:         true,
	}
	builder := NewBuilder(cfg)
	builder.now = func() time.Time { return time.Date(2026, 6, 4, 8, 0, 0, 0, time.UTC) }
	plan, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if strings.Join(plan.CoverageCryptos, ",") != "BNB-USDT,TRX,USDC,USDT" {
		t.Fatalf("unexpected coverage cryptos: %v", plan.CoverageCryptos)
	}
	if plan.Source != "env:FINAL_PLAN_CRYPTOS" || plan.GeneratedAt != "2026-06-04T08:00:00Z" {
		t.Fatalf("unexpected source/timestamp: source=%s generated=%s", plan.Source, plan.GeneratedAt)
	}
	if len(plan.WorkerURLs) != 2 || plan.WorkerURLs["bnb"] != "http://bnb-worker:6000" || plan.WorkerURLs["tron"] != "http://tron-worker:6000" {
		t.Fatalf("unexpected worker urls: %+v", plan.WorkerURLs)
	}
	workers := map[string]string{}
	for _, check := range plan.WorkerAddressChecks {
		workers[check.Worker] = check.Crypto
	}
	if workers["bnb"] != "BNB" || workers["tron"] != "TRX" {
		t.Fatalf("unexpected worker address checks: %+v", plan.WorkerAddressChecks)
	}
	paymentByName := map[string]struct{}{}
	for _, check := range plan.PaymentChecks {
		paymentByName[check.Name] = struct{}{}
	}
	for _, check := range plan.PayoutChecks {
		if check.Amount != "0.01" {
			t.Fatalf("payout amount not applied: %+v", check)
		}
		if check.Destination != "" {
			t.Fatalf("generated plan should use destination_from_payment, not static destination: %+v", check)
		}
		if _, ok := paymentByName[check.DestinationFromPayment]; !ok {
			t.Fatalf("payout references missing payment check: %+v", check)
		}
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	if !strings.Contains(string(data), "destination_from_payment") {
		t.Fatalf("serialized plan is missing destination_from_payment: %s", string(data))
	}
}

func TestBuildAllDefaultPlanCoversEveryDefaultCrypto(t *testing.T) {
	cfg := Config{
		AllDefaultCryptos:   true,
		UseWalletCryptos:    false,
		APIKey:              "replace-with-wallet-api-key",
		AdminUsername:       "admin",
		AdminPassword:       "replace-with-current-admin-password",
		AdminUpdateUsername: "admin",
		AdminUpdatePassword: "replace-with-staging-new-password",
		PaymentAmount:       "1",
		ReportFile:          "/reports/final.json",
		ExpectServerStatus:  "Synced",
		Mutating:            true,
		RequirePayment:      true,
		RequirePayout:       true,
	}
	plan, err := NewBuilder(cfg).Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(plan.CoverageCryptos) < 30 {
		t.Fatalf("expected broad default crypto coverage, got %d: %v", len(plan.CoverageCryptos), plan.CoverageCryptos)
	}
	if len(plan.PaymentChecks) != len(plan.CoverageCryptos) || len(plan.PayoutChecks) != len(plan.CoverageCryptos) {
		t.Fatalf("payment/payout coverage mismatch: coverage=%d payment=%d payout=%d", len(plan.CoverageCryptos), len(plan.PaymentChecks), len(plan.PayoutChecks))
	}
	for _, worker := range []string{"btc", "ltc", "doge", "firo", "lightning", "eth", "tron", "bnb", "polygon", "avalanche", "arbitrum", "optimism", "solana", "xmr", "xrp"} {
		if plan.WorkerURLs[worker] == "" {
			t.Fatalf("all-default plan missing worker url %s", worker)
		}
	}
	foundPlaceholder := false
	for _, check := range plan.PayoutChecks {
		if strings.HasPrefix(check.Amount, "replace-with-") {
			foundPlaceholder = true
			break
		}
	}
	if !foundPlaceholder {
		t.Fatalf("default final plan should keep payout amount placeholders")
	}
}

func TestDatabaseURLConfigRejectsNonMariaDB(t *testing.T) {
	for _, raw := range []string{
		"sqlite:////data/shkeeper.sqlite",
		"sqlite:///tmp/shkeeper.sqlite",
		"postgres://user:pass@db/shkeeper",
		"/data/shkeeper.sqlite",
	} {
		if err := databaseURLConfigError(raw); err == nil {
			t.Fatalf("expected database URL %q to be rejected", raw)
		}
	}
	for _, raw := range []string{
		"mariadb://root:pass@mariadb:3306/shkeeper",
		"mysql://root:pass@mariadb:3306/shkeeper",
		"root:pass@tcp(mariadb:3306)/shkeeper?parseTime=true",
	} {
		if err := databaseURLConfigError(raw); err != nil {
			t.Fatalf("expected database URL %q to be accepted: %v", raw, err)
		}
	}
}

func TestWorkerCredentialEnvFallbacks(t *testing.T) {
	t.Setenv("FINAL_PLAN_WORKER_USERNAME", "shared-user")
	t.Setenv("FINAL_PLAN_WORKER_PASSWORD", "shared-pass")
	t.Setenv("FINAL_PLAN_WORKER_USERNAME_TRON", "tron-user")
	t.Setenv("FINAL_PLAN_WORKER_PASSWORD_TRON", "tron-pass")

	cfg := Config{
		Cryptos:          []string{"BNB-USDT", "TRX"},
		UseWalletCryptos: false,
		PaymentAmount:    "1",
		PayoutAmount:     "0.01",
		Mutating:         true,
		RequirePayment:   true,
		RequirePayout:    true,
	}
	plan, err := NewBuilder(cfg).Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	credentials := map[string][2]string{}
	for _, check := range plan.WorkerAddressChecks {
		credentials[check.Worker] = [2]string{check.Username, check.Password}
	}
	if credentials["bnb"] != [2]string{"shared-user", "shared-pass"} {
		t.Fatalf("shared worker credential fallback not applied: %+v", credentials)
	}
	if credentials["tron"] != [2]string{"tron-user", "tron-pass"} {
		t.Fatalf("specific worker credential override not applied: %+v", credentials)
	}
}

func TestReadinessReportsPlaceholdersAndCoverage(t *testing.T) {
	plan, err := NewBuilder(Config{
		Cryptos:          []string{"BNB-USDT", "TRX"},
		UseWalletCryptos: false,
		PaymentAmount:    "1",
		Mutating:         true,
		RequirePayment:   true,
		RequirePayout:    true,
	}).Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	report := Readiness(plan)
	if report.Status != "blocked" || report.ReadyForDeployCheck || report.ReadyForCutoverAudit || report.ReadyForPostCutover {
		t.Fatalf("unexpected readiness status: %+v", report)
	}
	if len(report.CoverageCryptos) != 2 || report.PaymentCheckCount != 2 || report.PayoutCheckCount != 2 {
		t.Fatalf("coverage counts not populated: %+v", report)
	}
	placeholders := strings.Join(report.PlaceholderFields, ",")
	for _, want := range []string{"payout_checks[0].amount", "worker_address_checks[0].username"} {
		if !strings.Contains(placeholders, want) {
			t.Fatalf("placeholder report missing %s: %+v", want, report.PlaceholderFields)
		}
	}
	missing := strings.Join(report.MissingFields, ",")
	for _, want := range []string{"api_key", "admin_password"} {
		if !strings.Contains(missing, want) {
			t.Fatalf("missing-field report missing %s: %+v", want, report.MissingFields)
		}
	}
	if len(report.NextRequiredEnv) == 0 {
		t.Fatalf("readiness report should include next required env hints")
	}
	for _, key := range []string{"api_key", "admin_credentials", "worker_credentials", "payout_amounts"} {
		if report.NextRequiredEnv[key] == "" {
			t.Fatalf("readiness next env should include %s: %+v", key, report.NextRequiredEnv)
		}
	}
	for group, wantPrefix := range map[string]string{
		"worker_credentials": "worker_address_checks[0].username",
		"payout_amounts":     "payout_checks[0].amount",
	} {
		if !groupContainsPrefix(report.PlaceholderGroups[group], wantPrefix) {
			t.Fatalf("placeholder group %s missing %s: %+v", group, wantPrefix, report.PlaceholderGroups)
		}
	}
	if !groupContainsPrefix(report.MissingFieldGroups["api_key"], "api_key") || !groupContainsPrefix(report.MissingFieldGroups["admin_credentials"], "admin_password") {
		t.Fatalf("missing field groups not populated: %+v", report.MissingFieldGroups)
	}
}

func TestReadinessNextEnvOmitsFilledAPIKey(t *testing.T) {
	t.Setenv("FINAL_PLAN_WORKER_USERNAME", "replace-with-worker")
	plan, err := NewBuilder(Config{
		Cryptos:          []string{"TRX"},
		UseWalletCryptos: false,
		APIKey:           "api-key",
		PaymentAmount:    "1",
		Mutating:         true,
		RequirePayment:   true,
		RequirePayout:    true,
	}).Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	report := Readiness(plan)
	if report.NextRequiredEnv["api_key"] != "" {
		t.Fatalf("api key hint should be omitted when API key is filled: %+v", report.NextRequiredEnv)
	}
	if report.NextRequiredEnv["admin_credentials"] == "" || report.NextRequiredEnv["worker_credentials"] == "" || report.NextRequiredEnv["payout_amounts"] == "" {
		t.Fatalf("expected targeted remaining hints: %+v", report.NextRequiredEnv)
	}
}

func TestFinalPlanReadsSecretsFromFiles(t *testing.T) {
	dir := t.TempDir()
	writeSecret := func(name string, value string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatalf("write secret %s: %v", name, err)
		}
		return path
	}
	t.Setenv("FINAL_PLAN_API_KEY_FILE", writeSecret("api-key", "api-from-file"))
	t.Setenv("FINAL_PLAN_ADMIN_PASSWORD_FILE", writeSecret("admin-password", "admin-from-file"))
	t.Setenv("FINAL_PLAN_ADMIN_UPDATE_PASSWORD_FILE", writeSecret("admin-update-password", "admin-update-from-file"))
	t.Setenv("FINAL_PLAN_CRYPTOS", "BNB-USDT")
	t.Setenv("FINAL_PLAN_WORKER_USERNAME", "worker")
	t.Setenv("FINAL_PLAN_WORKER_PASSWORD_FILE", writeSecret("worker-password", "worker-from-file"))
	plan, err := NewBuilder(LoadConfigFromEnv()).Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if plan.APIKey != "api-from-file" || plan.AdminPassword != "admin-from-file" || plan.AdminUpdatePassword != "admin-update-from-file" {
		t.Fatalf("secret file values not loaded into plan")
	}
	if len(plan.WorkerAddressChecks) == 0 || plan.WorkerAddressChecks[0].Password != "worker-from-file" {
		t.Fatalf("worker password file not loaded: %+v", plan.WorkerAddressChecks)
	}
	report := Readiness(plan)
	if groupContainsPrefix(report.PlaceholderGroups["api_key"], "api_key") || groupContainsPrefix(report.PlaceholderGroups["admin_credentials"], "admin_password") || groupContainsPrefix(report.PlaceholderGroups["worker_credentials"], "worker_address_checks") {
		t.Fatalf("file-backed secrets should not be placeholders: %+v", report.PlaceholderGroups)
	}
}

func TestFinalPlanCanRedactFileSecretsWhileReadinessStaysReady(t *testing.T) {
	dir := t.TempDir()
	writeSecret := func(name string, value string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatalf("write secret %s: %v", name, err)
		}
		return path
	}
	t.Setenv("FINAL_PLAN_REDACT_SECRETS", "true")
	t.Setenv("FINAL_PLAN_API_KEY_FILE", writeSecret("api-key", "api-from-file"))
	t.Setenv("FINAL_PLAN_ADMIN_PASSWORD_FILE", writeSecret("admin-password", "admin-from-file"))
	t.Setenv("FINAL_PLAN_ADMIN_UPDATE_PASSWORD_FILE", writeSecret("admin-update-password", "admin-update-from-file"))
	t.Setenv("FINAL_PLAN_CRYPTOS", "BNB-USDT")
	t.Setenv("FINAL_PLAN_PAYOUT_AMOUNT", "0.01")
	t.Setenv("FINAL_PLAN_WORKER_USERNAME", "worker")
	t.Setenv("FINAL_PLAN_WORKER_PASSWORD_FILE", writeSecret("worker-password", "worker-from-file"))

	plan, err := NewBuilder(LoadConfigFromEnv()).Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, secret := range []string{"api-from-file", "admin-from-file", "admin-update-from-file", "worker-from-file"} {
		data, err := json.Marshal(plan)
		if err != nil {
			t.Fatalf("marshal plan: %v", err)
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("redacted plan leaked secret %q: %s", secret, string(data))
		}
	}
	report := Readiness(plan)
	if report.Status != "ready" || !report.ReadyForDeployCheck {
		t.Fatalf("secret-file-backed plan should be deploy-check ready: %+v", report)
	}
	if len(report.PlaceholderFields) != 0 || len(report.MissingFields) != 0 {
		t.Fatalf("secret override fields should not block readiness: %+v", report)
	}
	for _, want := range []string{"api_key", "admin_password", "admin_update_password", "worker_address_checks[0].password"} {
		if !contains(report.SecretOverrideFields, want) {
			t.Fatalf("readiness missing secret override %s: %+v", want, report.SecretOverrideFields)
		}
	}
}

func TestReadinessReadyWhenPlaceholdersAreReplaced(t *testing.T) {
	t.Setenv("FINAL_PLAN_WORKER_USERNAME", "worker")
	t.Setenv("FINAL_PLAN_WORKER_PASSWORD", "worker-pass")
	plan, err := NewBuilder(Config{
		Cryptos:             []string{"BNB-USDT"},
		UseWalletCryptos:    false,
		APIKey:              "api-key",
		AdminUsername:       "admin",
		AdminPassword:       "admin-pass",
		AdminUpdateUsername: "admin",
		AdminUpdatePassword: "admin-new-pass",
		PaymentAmount:       "1",
		PayoutAmount:        "0.01",
		Mutating:            true,
		RequirePayment:      true,
		RequirePayout:       true,
	}).Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	report := Readiness(plan)
	if report.Status != "ready" || !report.ReadyForDeployCheck || !report.ReadyForCutoverAudit || report.ReadyForPostCutover {
		t.Fatalf("unexpected readiness status: %+v", report)
	}
	if len(report.PlaceholderFields) != 0 || len(report.MissingFields) != 0 || len(report.MissingCoverage) != 0 || len(report.MissingWorkers) != 0 {
		t.Fatalf("ready report should not include blockers: %+v", report)
	}
}

func TestReadinessBlocksWhenOrderStatusMatrixIsDisabled(t *testing.T) {
	t.Setenv("FINAL_PLAN_WORKER_USERNAME", "worker")
	t.Setenv("FINAL_PLAN_WORKER_PASSWORD", "worker-pass")
	plan, err := NewBuilder(Config{
		Cryptos:             []string{"BNB-USDT"},
		UseWalletCryptos:    false,
		APIKey:              "api-key",
		AdminUsername:       "admin",
		AdminPassword:       "admin-pass",
		AdminUpdateUsername: "admin",
		AdminUpdatePassword: "admin-new-pass",
		PaymentAmount:       "1",
		PayoutAmount:        "0.01",
		Mutating:            true,
		RequirePayment:      true,
		RequirePayout:       true,
	}).Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	plan.OrderStatusMatrixCheck = false
	report := Readiness(plan)
	if report.Status != "blocked" || report.ReadyForDeployCheck || report.OrderStatusMatrix {
		t.Fatalf("expected blocked readiness without order matrix: %+v", report)
	}
	if !strings.Contains(strings.Join(report.MissingFields, ","), "order_status_matrix_check") {
		t.Fatalf("missing fields should mention order matrix: %+v", report.MissingFields)
	}
}

func TestLoadConfigUsesAdminAccountEnvFallbacks(t *testing.T) {
	t.Setenv("FINAL_PLAN_ADMIN_USERNAME", "")
	t.Setenv("FINAL_PLAN_ADMIN_PASSWORD", "")
	t.Setenv("FINAL_PLAN_ADMIN_UPDATE_USERNAME", "")
	t.Setenv("FINAL_PLAN_ADMIN_UPDATE_PASSWORD", "")
	t.Setenv("ADMIN_USERNAME", "cutover-admin")
	t.Setenv("ADMIN_PASSWORD", "cutover-admin-pass")
	t.Setenv("ADMIN_UPDATE_USERNAME", "cutover-admin-next")
	t.Setenv("ADMIN_UPDATE_PASSWORD", "cutover-admin-next-pass")

	cfg := LoadConfigFromEnv()
	if cfg.AdminUsername != "cutover-admin" || cfg.AdminPassword != "cutover-admin-pass" ||
		cfg.AdminUpdateUsername != "cutover-admin-next" || cfg.AdminUpdatePassword != "cutover-admin-next-pass" {
		t.Fatalf("admin env fallbacks not applied: %+v", cfg)
	}
}

func TestFinalPlanCanUseMariaDBWalletAPIKey(t *testing.T) {
	db := openFinalPlanFakeDB(t)
	cfg := Config{
		Cryptos:             []string{"BNB-USDT"},
		UseWalletCryptos:    false,
		UseWalletAPIKey:     true,
		APIKey:              "replace-with-wallet-api-key",
		AdminUsername:       "admin",
		AdminPassword:       "replace-with-current-admin-password",
		AdminUpdateUsername: "admin",
		AdminUpdatePassword: "replace-with-staging-new-password",
		PaymentAmount:       "1",
		PayoutAmount:        "0.01",
		Mutating:            true,
		RequirePayment:      true,
		RequirePayout:       true,
	}
	builder := NewBuilder(cfg)
	builder.open = func(context.Context, Config) (*sql.DB, error) {
		return db, nil
	}
	plan, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if plan.APIKey != "db-api-key" {
		t.Fatalf("expected API key from MariaDB, got %q", plan.APIKey)
	}
	report := Readiness(plan)
	if strings.Contains(strings.Join(report.PlaceholderFields, ","), "api_key") {
		t.Fatalf("readiness should not report api_key placeholder after DB fill: %+v", report.PlaceholderFields)
	}
}

func TestExplicitFinalPlanAPIKeyWinsOverMariaDBLookup(t *testing.T) {
	cfg := Config{
		Cryptos:          []string{"BNB-USDT"},
		UseWalletCryptos: false,
		UseWalletAPIKey:  true,
		APIKey:           "explicit-api-key",
		PaymentAmount:    "1",
		Mutating:         false,
	}
	builder := NewBuilder(cfg)
	builder.open = func(context.Context, Config) (*sql.DB, error) {
		t.Fatal("MariaDB should not be opened when FINAL_PLAN_API_KEY is explicit")
		return nil, nil
	}
	plan, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if plan.APIKey != "explicit-api-key" {
		t.Fatalf("explicit API key should win, got %q", plan.APIKey)
	}
}

func TestFinalPlanCanUseMariaDBWalletServerKey(t *testing.T) {
	db := openFinalPlanFakeDB(t)
	cfg := Config{
		Cryptos:             []string{"BNB-USDT"},
		UseWalletCryptos:    false,
		UseWalletServerKey:  true,
		APIKey:              "api-key",
		AdminUsername:       "admin",
		AdminPassword:       "replace-with-current-admin-password",
		AdminUpdateUsername: "admin",
		AdminUpdatePassword: "replace-with-staging-new-password",
		PaymentAmount:       "1",
		PayoutAmount:        "0.01",
		Mutating:            true,
		RequirePayment:      true,
		RequirePayout:       true,
	}
	builder := NewBuilder(cfg)
	builder.open = func(context.Context, Config) (*sql.DB, error) {
		return db, nil
	}
	plan, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(plan.WorkerAddressChecks) != 1 {
		t.Fatalf("unexpected worker checks: %+v", plan.WorkerAddressChecks)
	}
	check := plan.WorkerAddressChecks[0]
	if check.Username != "db-worker" || check.Password != "db-worker-pass" {
		t.Fatalf("expected worker credentials from wallet.serverkey, got %+v", check)
	}
	report := Readiness(plan)
	placeholders := strings.Join(report.PlaceholderFields, ",")
	if strings.Contains(placeholders, "worker_address_checks[0].username") || strings.Contains(placeholders, "worker_address_checks[0].password") {
		t.Fatalf("readiness should not report worker credential placeholders after DB fill: %+v", report.PlaceholderFields)
	}
}

func TestExplicitWorkerCredentialsWinOverMariaDBServerKey(t *testing.T) {
	t.Setenv("FINAL_PLAN_WORKER_USERNAME_BNB", "explicit-worker")
	t.Setenv("FINAL_PLAN_WORKER_PASSWORD_BNB", "explicit-pass")
	cfg := Config{
		Cryptos:            []string{"BNB-USDT"},
		UseWalletCryptos:   false,
		UseWalletServerKey: true,
		APIKey:             "api-key",
		PaymentAmount:      "1",
	}
	builder := NewBuilder(cfg)
	builder.open = func(context.Context, Config) (*sql.DB, error) {
		t.Fatal("MariaDB should not be opened when worker credentials are explicit")
		return nil, nil
	}
	plan, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	check := plan.WorkerAddressChecks[0]
	if check.Username != "explicit-worker" || check.Password != "explicit-pass" {
		t.Fatalf("explicit worker credentials should win, got %+v", check)
	}
}

func TestWriteReadinessWritesJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readiness.json")
	report := ReadinessReport{
		Status:              "blocked",
		CoverageCryptos:     []string{"BTC"},
		ReadyForDeployCheck: false,
	}
	if err := WriteReadiness(path, report); err != nil {
		t.Fatalf("WriteReadiness() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read readiness report: %v", err)
	}
	if !strings.Contains(string(data), `"status": "blocked"`) {
		t.Fatalf("unexpected readiness JSON: %s", string(data))
	}
}

func groupContainsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var registerFinalPlanFakeDriver sync.Once

func openFinalPlanFakeDB(t *testing.T) *sql.DB {
	t.Helper()
	registerFinalPlanFakeDriver.Do(func() {
		sql.Register("finalplan_fake", finalPlanFakeDriver{})
	})
	db, err := sql.Open("finalplan_fake", "")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type finalPlanFakeDriver struct{}

func (finalPlanFakeDriver) Open(string) (driver.Conn, error) {
	return finalPlanFakeConn{}, nil
}

type finalPlanFakeConn struct{}

func (finalPlanFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, os.ErrInvalid
}

func (finalPlanFakeConn) Close() error {
	return nil
}

func (finalPlanFakeConn) Begin() (driver.Tx, error) {
	return nil, os.ErrInvalid
}

func (finalPlanFakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "SELECT apikey FROM wallet"):
		return &finalPlanFakeRows{columns: []string{"apikey"}, values: [][]driver.Value{{"db-api-key"}}}, nil
	case strings.Contains(query, "SELECT serverkey FROM wallet"):
		return &finalPlanFakeRows{columns: []string{"serverkey"}, values: [][]driver.Value{{"db-worker:db-worker-pass"}}}, nil
	case strings.Contains(query, "SELECT crypto FROM wallet"):
		return &finalPlanFakeRows{columns: []string{"crypto"}, values: [][]driver.Value{{"BNB-USDT"}}}, nil
	default:
		return &finalPlanFakeRows{columns: []string{"empty"}, values: nil}, nil
	}
}

type finalPlanFakeRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *finalPlanFakeRows) Columns() []string {
	return r.columns
}

func (r *finalPlanFakeRows) Close() error {
	return nil
}

func (r *finalPlanFakeRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
