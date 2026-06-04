package finalplan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/app"
	"github.com/Sky-JD/go-shkeeper/internal/deploycheck"
)

type Config struct {
	MainURL               string
	Cryptos               []string
	AllDefaultCryptos     bool
	UseWalletCryptos      bool
	UseWalletAPIKey       bool
	UseWalletServerKey    bool
	DatabaseURL           string
	OutputFile            string
	ReadinessFile         string
	APIKey                string
	AdminUsername         string
	AdminPassword         string
	AdminUpdateUsername   string
	AdminUpdatePassword   string
	PaymentAmount         string
	PayoutAmount          string
	ReportFile            string
	ExpectServerStatus    string
	OrderRequests         int
	OrderConcurrency      int
	OrderMaxLatencyMS     int
	OrderListRequests     int
	OrderListConcurrency  int
	OrderListMaxLatencyMS int
	Requests              int
	Concurrency           int
	TimeoutSeconds        int
	Mutating              bool
	RequirePayment        bool
	RequirePayout         bool
	RedactSecrets         bool
}

type Plan struct {
	MainURL                string                           `json:"main_url"`
	WorkerURLs             map[string]string                `json:"worker_urls"`
	APIKey                 string                           `json:"api_key"`
	Crypto                 string                           `json:"crypto"`
	Cryptos                []string                         `json:"cryptos"`
	CoverageCryptos        []string                         `json:"coverage_cryptos"`
	MainStatusCheck        bool                             `json:"main_status_check"`
	ExpectServerStatus     string                           `json:"expect_server_status"`
	OrderExternalID        string                           `json:"order_external_id,omitempty"`
	ExpectStatus           string                           `json:"expect_status"`
	OrderRequests          int                              `json:"order_requests"`
	OrderConcurrency       int                              `json:"order_concurrency"`
	OrderMaxLatencyMS      int                              `json:"order_max_latency_ms"`
	OrderListCheck         bool                             `json:"order_list_check"`
	OrderListStatus        string                           `json:"order_list_status"`
	OrderListCrypto        string                           `json:"order_list_crypto"`
	OrderListLimit         int                              `json:"order_list_limit"`
	OrderListMinResults    int                              `json:"order_list_min_results"`
	OrderListRequests      int                              `json:"order_list_requests"`
	OrderListConcurrency   int                              `json:"order_list_concurrency"`
	OrderListMaxLatencyMS  int                              `json:"order_list_max_latency_ms"`
	OrderStatusMatrixCheck bool                             `json:"order_status_matrix_check"`
	OrderStatusMatrixLimit int                              `json:"order_status_matrix_limit"`
	OrderStatusMatrixPages int                              `json:"order_status_matrix_pages"`
	OrderStatusMatrixMin   int                              `json:"order_status_matrix_min"`
	Requests               int                              `json:"requests"`
	Concurrency            int                              `json:"concurrency"`
	TimeoutSeconds         int                              `json:"timeout_seconds"`
	ReportFile             string                           `json:"report_file"`
	AdminUsername          string                           `json:"admin_username"`
	AdminPassword          string                           `json:"admin_password"`
	AdminUpdateUsername    string                           `json:"admin_update_username"`
	AdminUpdatePassword    string                           `json:"admin_update_password"`
	RequirePaymentCoverage bool                             `json:"require_payment_coverage"`
	RequirePayoutCoverage  bool                             `json:"require_payout_coverage"`
	Mutating               bool                             `json:"mutating"`
	PaymentChecks          []deploycheck.PaymentCheck       `json:"payment_checks"`
	PayoutChecks           []deploycheck.PayoutCheck        `json:"payout_checks"`
	WorkerAddressChecks    []deploycheck.WorkerAddressCheck `json:"worker_address_checks"`
	Source                 string                           `json:"source,omitempty"`
	GeneratedAt            string                           `json:"generated_at,omitempty"`
	Notes                  []string                         `json:"notes,omitempty"`
	WorkerCryptoMap        map[string]string                `json:"worker_crypto_map,omitempty"`
	Placeholders           map[string]string                `json:"placeholders,omitempty"`
	SecretOverrideFields   []string                         `json:"secret_override_fields,omitempty"`
}

type Builder struct {
	cfg  Config
	now  func() time.Time
	open func(context.Context, Config) (*sql.DB, error)
}

type ReadinessReport struct {
	Status               string              `json:"status"`
	GeneratedAt          string              `json:"generated_at,omitempty"`
	Source               string              `json:"source,omitempty"`
	CoverageCryptos      []string            `json:"coverage_cryptos"`
	Workers              []string            `json:"workers"`
	PaymentCheckCount    int                 `json:"payment_check_count"`
	PayoutCheckCount     int                 `json:"payout_check_count"`
	WorkerAddressCount   int                 `json:"worker_address_count"`
	Mutating             bool                `json:"mutating"`
	RequirePayment       bool                `json:"require_payment_coverage"`
	RequirePayout        bool                `json:"require_payout_coverage"`
	OrderStatusMatrix    bool                `json:"order_status_matrix_check"`
	OrderExternalID      string              `json:"order_external_id,omitempty"`
	OrderListCrypto      string              `json:"order_list_crypto,omitempty"`
	PlaceholderFields    []string            `json:"placeholder_fields,omitempty"`
	PlaceholderGroups    map[string][]string `json:"placeholder_groups,omitempty"`
	MissingFields        []string            `json:"missing_fields,omitempty"`
	MissingFieldGroups   map[string][]string `json:"missing_field_groups,omitempty"`
	MissingCoverage      []string            `json:"missing_coverage,omitempty"`
	MissingWorkers       []string            `json:"missing_workers,omitempty"`
	Warnings             []string            `json:"warnings,omitempty"`
	NextRequiredEnv      map[string]string   `json:"next_required_env,omitempty"`
	SecretOverrideFields []string            `json:"secret_override_fields,omitempty"`
	ReadyForDeployCheck  bool                `json:"ready_for_deploy_check"`
	ReadyForCutoverAudit bool                `json:"ready_for_cutover_audit"`
	ReadyForPostCutover  bool                `json:"ready_for_post_cutover"`
}

func LoadConfigFromEnv() Config {
	cfg := Config{
		MainURL:               env("FINAL_PLAN_MAIN_URL", "http://shkeeper:5000"),
		Cryptos:               splitCSV(os.Getenv("FINAL_PLAN_CRYPTOS")),
		AllDefaultCryptos:     boolEnv("FINAL_PLAN_ALL_CRYPTOS", false),
		UseWalletCryptos:      boolEnv("FINAL_PLAN_USE_WALLET_CRYPTOS", firstEnv("MARIADB_DATABASE_URL", "DATABASE_URL") != ""),
		UseWalletAPIKey:       boolEnv("FINAL_PLAN_USE_WALLET_API_KEY", false),
		UseWalletServerKey:    boolEnv("FINAL_PLAN_USE_WALLET_SERVERKEY", false),
		DatabaseURL:           firstEnv("FINAL_PLAN_MARIADB_DATABASE_URL", "MARIADB_DATABASE_URL", "DATABASE_URL"),
		OutputFile:            strings.TrimSpace(os.Getenv("FINAL_PLAN_OUTPUT_FILE")),
		ReadinessFile:         strings.TrimSpace(os.Getenv("FINAL_PLAN_READINESS_FILE")),
		APIKey:                secretEnv("FINAL_PLAN_API_KEY", "replace-with-wallet-api-key"),
		AdminUsername:         firstNonEmptyEnv("FINAL_PLAN_ADMIN_USERNAME", "ADMIN_USERNAME", "ADMIN_ACCOUNT_USERNAME", "admin"),
		AdminPassword:         firstNonEmptySecretEnv("FINAL_PLAN_ADMIN_PASSWORD", "ADMIN_PASSWORD", "replace-with-current-admin-password"),
		AdminUpdateUsername:   firstNonEmptyEnv("FINAL_PLAN_ADMIN_UPDATE_USERNAME", "ADMIN_UPDATE_USERNAME", "admin"),
		AdminUpdatePassword:   firstNonEmptySecretEnv("FINAL_PLAN_ADMIN_UPDATE_PASSWORD", "ADMIN_UPDATE_PASSWORD", "replace-with-staging-new-password"),
		PaymentAmount:         env("FINAL_PLAN_PAYMENT_AMOUNT", "1"),
		PayoutAmount:          env("FINAL_PLAN_PAYOUT_AMOUNT", ""),
		ReportFile:            env("FINAL_PLAN_REPORT_FILE", "/deploy-reports/go-shkeeper-final-deploy-check-report.json"),
		ExpectServerStatus:    env("FINAL_PLAN_EXPECT_SERVER_STATUS", "Synced"),
		OrderRequests:         intEnv("FINAL_PLAN_ORDER_REQUESTS", 100),
		OrderConcurrency:      intEnv("FINAL_PLAN_ORDER_CONCURRENCY", 20),
		OrderMaxLatencyMS:     intEnv("FINAL_PLAN_ORDER_MAX_LATENCY_MS", 500),
		OrderListRequests:     intEnv("FINAL_PLAN_ORDER_LIST_REQUESTS", 100),
		OrderListConcurrency:  intEnv("FINAL_PLAN_ORDER_LIST_CONCURRENCY", 20),
		OrderListMaxLatencyMS: intEnv("FINAL_PLAN_ORDER_LIST_MAX_LATENCY_MS", 500),
		Requests:              intEnv("FINAL_PLAN_REQUESTS", 100),
		Concurrency:           intEnv("FINAL_PLAN_CONCURRENCY", 20),
		TimeoutSeconds:        intEnv("FINAL_PLAN_TIMEOUT_SECONDS", 45),
		Mutating:              boolEnv("FINAL_PLAN_MUTATING", true),
		RequirePayment:        boolEnv("FINAL_PLAN_REQUIRE_PAYMENT_COVERAGE", true),
		RequirePayout:         boolEnv("FINAL_PLAN_REQUIRE_PAYOUT_COVERAGE", true),
		RedactSecrets:         boolEnv("FINAL_PLAN_REDACT_SECRETS", false),
	}
	return cfg
}

func NewBuilder(cfg Config) Builder {
	return Builder{
		cfg: cfg,
		now: time.Now,
		open: func(ctx context.Context, cfg Config) (*sql.DB, error) {
			appCfg := app.LoadConfig()
			if strings.TrimSpace(cfg.DatabaseURL) != "" {
				if err := databaseURLConfigError(cfg.DatabaseURL); err != nil {
					return nil, err
				}
				appCfg.DatabaseURL = cfg.DatabaseURL
				appCfg.DatabaseConfigError = ""
				appCfg.DatabaseDriver, appCfg.DatabaseDSN = parseDatabaseURLCompat(cfg.DatabaseURL)
			}
			store, err := app.OpenStore(ctx, appCfg, nil)
			if err != nil {
				return nil, err
			}
			return store.DB(), nil
		},
	}
}

func (b Builder) Build(ctx context.Context) (Plan, error) {
	defs := app.CryptoDefinitions()
	defByCrypto := map[string]app.CryptoModule{}
	for _, def := range defs {
		defByCrypto[def.Name] = def
	}
	cryptos, source, db, err := b.resolveCryptos(ctx, defs)
	if err != nil {
		return Plan{}, err
	}
	if db != nil {
		defer db.Close()
	}
	if len(cryptos) == 0 {
		return Plan{}, fmt.Errorf("final plan requires at least one crypto")
	}
	orderExternalID, orderListCrypto := "", cryptos[0]
	if db != nil {
		orderExternalID, orderListCrypto = b.pickOrderEvidence(ctx, db, cryptos)
		if orderListCrypto == "" {
			orderListCrypto = cryptos[0]
		}
	}
	apiKey, err := b.resolveAPIKey(ctx, db, cryptos)
	if err != nil {
		return Plan{}, err
	}
	workerURLs, workerChecks, workerMap := b.workerCoverage(cryptos, defByCrypto)
	workerChecks, err = b.resolveWorkerCredentials(ctx, db, cryptos, defByCrypto, workerChecks)
	if err != nil {
		return Plan{}, err
	}
	paymentChecks := make([]deploycheck.PaymentCheck, 0, len(cryptos))
	payoutChecks := make([]deploycheck.PayoutCheck, 0, len(cryptos))
	for _, crypto := range cryptos {
		paymentName := "payment-" + slug(crypto)
		paymentChecks = append(paymentChecks, deploycheck.PaymentCheck{
			Name:             paymentName,
			Crypto:           crypto,
			Fiat:             "USD",
			Amount:           b.cfg.PaymentAmount,
			ExternalIDPrefix: "final-cutover-payment-" + slug(crypto),
			ExpectStatus:     "UNPAID",
		})
		payoutChecks = append(payoutChecks, deploycheck.PayoutCheck{
			Name:                   "payout-" + slug(crypto),
			Crypto:                 crypto,
			DestinationFromPayment: paymentName,
			Amount:                 b.payoutAmount(crypto),
			ExternalIDPrefix:       "final-cutover-payout-" + slug(crypto),
			ExpectStatus:           "IN_PROGRESS",
		})
	}
	plan := Plan{
		MainURL:                strings.TrimSpace(b.cfg.MainURL),
		WorkerURLs:             workerURLs,
		APIKey:                 apiKey,
		Crypto:                 cryptos[0],
		Cryptos:                append([]string(nil), cryptos...),
		CoverageCryptos:        append([]string(nil), cryptos...),
		MainStatusCheck:        true,
		ExpectServerStatus:     b.cfg.ExpectServerStatus,
		OrderExternalID:        orderExternalID,
		ExpectStatus:           "UNPAID",
		OrderRequests:          positive(b.cfg.OrderRequests, 100),
		OrderConcurrency:       positive(b.cfg.OrderConcurrency, 20),
		OrderMaxLatencyMS:      positive(b.cfg.OrderMaxLatencyMS, 500),
		OrderListCheck:         true,
		OrderListStatus:        "UNPAID",
		OrderListCrypto:        orderListCrypto,
		OrderListLimit:         50,
		OrderListMinResults:    1,
		OrderListRequests:      positive(b.cfg.OrderListRequests, 100),
		OrderListConcurrency:   positive(b.cfg.OrderListConcurrency, 20),
		OrderListMaxLatencyMS:  positive(b.cfg.OrderListMaxLatencyMS, 500),
		OrderStatusMatrixCheck: true,
		OrderStatusMatrixLimit: 100,
		OrderStatusMatrixPages: 20,
		OrderStatusMatrixMin:   1,
		Requests:               positive(b.cfg.Requests, 100),
		Concurrency:            positive(b.cfg.Concurrency, 20),
		TimeoutSeconds:         positive(b.cfg.TimeoutSeconds, 45),
		ReportFile:             b.cfg.ReportFile,
		AdminUsername:          b.cfg.AdminUsername,
		AdminPassword:          b.cfg.AdminPassword,
		AdminUpdateUsername:    b.cfg.AdminUpdateUsername,
		AdminUpdatePassword:    b.cfg.AdminUpdatePassword,
		RequirePaymentCoverage: b.cfg.RequirePayment,
		RequirePayoutCoverage:  b.cfg.RequirePayout,
		Mutating:               b.cfg.Mutating,
		PaymentChecks:          paymentChecks,
		PayoutChecks:           payoutChecks,
		WorkerAddressChecks:    workerChecks,
		Source:                 source,
		GeneratedAt:            b.now().UTC().Format(time.RFC3339),
		WorkerCryptoMap:        workerMap,
		Placeholders: map[string]string{
			"payout_amount": "Set FINAL_PLAN_PAYOUT_AMOUNT or FINAL_PLAN_PAYOUT_AMOUNT_<CRYPTO> before running mutating checks.",
			"worker_auth":   "Set FINAL_PLAN_WORKER_USERNAME_<WORKER> and FINAL_PLAN_WORKER_PASSWORD_<WORKER>, or edit worker_address_checks.",
		},
	}
	b.applySecretRedaction(&plan)
	if orderExternalID == "" {
		plan.Notes = append(plan.Notes, "No existing unpaid order was found in MariaDB; deploy-check will skip the standalone order_external_id lookup, while payment/payout checks still verify complete order details.")
	}
	return plan, nil
}

func (b Builder) applySecretRedaction(plan *Plan) {
	if !b.cfg.RedactSecrets || plan == nil {
		return
	}
	fields := make([]string, 0)
	redact := func(field string, value *string, placeholder string, ok bool) {
		if !ok || value == nil || strings.TrimSpace(*value) == "" || hasPlanPlaceholder(*value) {
			return
		}
		*value = placeholder
		fields = append(fields, field)
	}
	redact("api_key", &plan.APIKey, "replace-with-wallet-api-key", secretFileEnvSet("FINAL_PLAN_API_KEY"))
	redact("admin_password", &plan.AdminPassword, "replace-with-current-admin-password", secretFileEnvSet("FINAL_PLAN_ADMIN_PASSWORD") || secretFileEnvSet("ADMIN_PASSWORD"))
	redact("admin_update_password", &plan.AdminUpdatePassword, "replace-with-staging-new-password", secretFileEnvSet("FINAL_PLAN_ADMIN_UPDATE_PASSWORD") || secretFileEnvSet("ADMIN_UPDATE_PASSWORD"))
	for i := range plan.WorkerAddressChecks {
		check := &plan.WorkerAddressChecks[i]
		if !workerSecretFileEnvSet("FINAL_PLAN_WORKER_PASSWORD", check.Worker) {
			continue
		}
		redact(
			fmt.Sprintf("worker_address_checks[%d].password", i),
			&check.Password,
			"replace-with-"+strings.ToLower(strings.TrimSpace(check.Worker))+"-worker-password",
			true,
		)
	}
	if len(fields) == 0 {
		return
	}
	sort.Strings(fields)
	plan.SecretOverrideFields = fields
}

func (b Builder) resolveCryptos(ctx context.Context, defs []app.CryptoModule) ([]string, string, *sql.DB, error) {
	if b.cfg.AllDefaultCryptos {
		return allDefaultCryptos(defs), "all-default", nil, nil
	}
	if len(b.cfg.Cryptos) > 0 {
		return normalizeCryptos(b.cfg.Cryptos), "env:FINAL_PLAN_CRYPTOS", nil, nil
	}
	if b.cfg.UseWalletCryptos {
		db, err := b.open(ctx, b.cfg)
		if err != nil {
			return nil, "", nil, fmt.Errorf("open MariaDB for wallet cryptos: %w", err)
		}
		cryptos, err := enabledWalletCryptos(ctx, db)
		if err != nil {
			_ = db.Close()
			return nil, "", nil, fmt.Errorf("query enabled wallet cryptos: %w", err)
		}
		if len(cryptos) > 0 {
			return cryptos, "mariadb:wallet.enabled", db, nil
		}
		_ = db.Close()
	}
	return allDefaultCryptos(defs), "all-default", nil, nil
}

func (b Builder) pickOrderEvidence(ctx context.Context, db *sql.DB, cryptos []string) (string, string) {
	if db == nil || len(cryptos) == 0 {
		return "", ""
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(cryptos)), ",")
	args := make([]any, 0, len(cryptos))
	for _, crypto := range cryptos {
		args = append(args, crypto)
	}
	query := "SELECT external_id, crypto FROM invoice WHERE status='UNPAID' AND external_id IS NOT NULL AND external_id <> '' AND crypto IN (" + placeholders + ") ORDER BY id DESC LIMIT 1"
	var externalID, crypto string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&externalID, &crypto); err == nil {
		return strings.TrimSpace(externalID), strings.TrimSpace(crypto)
	}
	query = "SELECT crypto FROM invoice WHERE status='UNPAID' AND crypto IN (" + placeholders + ") GROUP BY crypto ORDER BY COUNT(*) DESC LIMIT 1"
	if err := db.QueryRowContext(ctx, query, args...).Scan(&crypto); err == nil {
		return "", strings.TrimSpace(crypto)
	}
	return "", ""
}

func (b Builder) resolveAPIKey(ctx context.Context, db *sql.DB, cryptos []string) (string, error) {
	apiKey := strings.TrimSpace(b.cfg.APIKey)
	if !b.cfg.UseWalletAPIKey || (apiKey != "" && !hasPlanPlaceholder(apiKey)) {
		return apiKey, nil
	}
	if db != nil {
		key, err := walletAPIKey(ctx, db, cryptos)
		if err != nil {
			return "", err
		}
		return key, nil
	}
	opened, err := b.open(ctx, b.cfg)
	if err != nil {
		return "", fmt.Errorf("open MariaDB for wallet API key: %w", err)
	}
	defer opened.Close()
	key, err := walletAPIKey(ctx, opened, cryptos)
	if err != nil {
		return "", err
	}
	return key, nil
}

func walletAPIKey(ctx context.Context, db *sql.DB, cryptos []string) (string, error) {
	if db == nil {
		return "", errors.New("wallet API key lookup requires MariaDB")
	}
	cryptos = normalizeCryptos(cryptos)
	args := []any{}
	where := "enabled = 1 AND apikey IS NOT NULL AND apikey <> ''"
	if len(cryptos) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(cryptos)), ",")
		where += " AND crypto IN (" + placeholders + ")"
		for _, crypto := range cryptos {
			args = append(args, crypto)
		}
	}
	query := "SELECT apikey FROM wallet WHERE " + where + " ORDER BY crypto LIMIT 1"
	var apiKey string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&apiKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("enabled MariaDB wallets do not contain an apikey; set FINAL_PLAN_API_KEY or populate wallet.apikey")
		}
		return "", fmt.Errorf("query wallet API key: %w", err)
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", errors.New("enabled MariaDB wallet apikey is empty; set FINAL_PLAN_API_KEY or populate wallet.apikey")
	}
	return apiKey, nil
}

func (b Builder) resolveWorkerCredentials(ctx context.Context, db *sql.DB, cryptos []string, defByCrypto map[string]app.CryptoModule, checks []deploycheck.WorkerAddressCheck) ([]deploycheck.WorkerAddressCheck, error) {
	if !b.cfg.UseWalletServerKey || len(checks) == 0 {
		return checks, nil
	}
	if workerCredentialsComplete(checks) {
		return checks, nil
	}
	workerCryptos := cryptosByWorker(cryptos, defByCrypto)
	apply := func(opened *sql.DB) error {
		for i := range checks {
			check := &checks[i]
			if !hasPlanPlaceholder(check.Username) && strings.TrimSpace(check.Username) != "" && !hasPlanPlaceholder(check.Password) && strings.TrimSpace(check.Password) != "" {
				continue
			}
			candidates := append([]string{check.Crypto}, workerCryptos[strings.TrimSpace(check.Worker)]...)
			key, err := firstWalletServerKey(ctx, opened, candidates)
			if err != nil {
				return err
			}
			if key == "" {
				continue
			}
			username, password, ok := serverKeyCredentials(key, check.Username)
			if !ok {
				continue
			}
			if hasPlanPlaceholder(check.Username) || strings.TrimSpace(check.Username) == "" {
				check.Username = username
			}
			if hasPlanPlaceholder(check.Password) || strings.TrimSpace(check.Password) == "" {
				check.Password = password
			}
		}
		return nil
	}
	if db != nil {
		if err := apply(db); err != nil {
			return nil, err
		}
		return checks, nil
	}
	opened, err := b.open(ctx, b.cfg)
	if err != nil {
		return nil, fmt.Errorf("open MariaDB for wallet serverkey: %w", err)
	}
	defer opened.Close()
	if err := apply(opened); err != nil {
		return nil, err
	}
	return checks, nil
}

func workerCredentialsComplete(checks []deploycheck.WorkerAddressCheck) bool {
	for _, check := range checks {
		if strings.TrimSpace(check.Username) == "" || strings.TrimSpace(check.Password) == "" || hasPlanPlaceholder(check.Username) || hasPlanPlaceholder(check.Password) {
			return false
		}
	}
	return true
}

func cryptosByWorker(cryptos []string, defByCrypto map[string]app.CryptoModule) map[string][]string {
	out := map[string][]string{}
	seen := map[string]map[string]struct{}{}
	for _, crypto := range cryptos {
		crypto = strings.ToUpper(strings.TrimSpace(crypto))
		def, ok := defByCrypto[crypto]
		if !ok || def.DefaultHost == "" {
			continue
		}
		worker := workerName(def.DefaultHost)
		if worker == "" {
			continue
		}
		if seen[worker] == nil {
			seen[worker] = map[string]struct{}{}
		}
		if _, ok := seen[worker][crypto]; ok {
			continue
		}
		seen[worker][crypto] = struct{}{}
		out[worker] = append(out[worker], crypto)
	}
	return out
}

func firstWalletServerKey(ctx context.Context, db *sql.DB, cryptos []string) (string, error) {
	cryptos = normalizeCryptos(cryptos)
	if len(cryptos) == 0 {
		return "", nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(cryptos)), ",")
	args := make([]any, 0, len(cryptos))
	for _, crypto := range cryptos {
		args = append(args, crypto)
	}
	query := "SELECT serverkey FROM wallet WHERE enabled = 1 AND serverkey IS NOT NULL AND serverkey <> '' AND crypto IN (" + placeholders + ") ORDER BY FIELD(crypto, " + placeholders + ") LIMIT 1"
	args = append(args, args...)
	var key string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("query wallet serverkey: %w", err)
	}
	return strings.TrimSpace(key), nil
}

func serverKeyCredentials(key string, currentUsername string) (string, string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}
	if username, password, ok := strings.Cut(key, ":"); ok {
		username = strings.TrimSpace(username)
		password = strings.TrimSpace(password)
		if username != "" && password != "" {
			return username, password, true
		}
		return "", "", false
	}
	username := strings.TrimSpace(currentUsername)
	if username == "" || hasPlanPlaceholder(username) {
		return "", "", false
	}
	return username, key, true
}

func (b Builder) workerCoverage(cryptos []string, defByCrypto map[string]app.CryptoModule) (map[string]string, []deploycheck.WorkerAddressCheck, map[string]string) {
	workerURLs := map[string]string{}
	workerProofCrypto := map[string]string{}
	for _, crypto := range cryptos {
		def, ok := defByCrypto[crypto]
		if !ok || def.DefaultHost == "" {
			continue
		}
		worker := workerName(def.DefaultHost)
		workerURLs[worker] = "http://" + def.DefaultHost + ":" + defaultPort(def)
		if _, ok := workerProofCrypto[worker]; !ok {
			workerProofCrypto[worker] = nativeCrypto(def)
		}
	}
	workers := make([]string, 0, len(workerProofCrypto))
	for worker := range workerProofCrypto {
		workers = append(workers, worker)
	}
	sort.Strings(workers)
	checks := make([]deploycheck.WorkerAddressCheck, 0, len(workers))
	for _, worker := range workers {
		crypto := workerProofCrypto[worker]
		check := deploycheck.WorkerAddressCheck{
			Name:     worker + "-address-proof",
			Worker:   worker,
			Crypto:   crypto,
			Username: workerCredential("FINAL_PLAN_WORKER_USERNAME", worker, "replace-with-"+worker+"-worker-user"),
			Password: workerCredential("FINAL_PLAN_WORKER_PASSWORD", worker, "replace-with-"+worker+"-worker-password"),
		}
		if crypto == "BTC-LIGHTNING" {
			check.Name = worker + "-invoice-proof"
			check.Amount = env(workerEnv("FINAL_PLAN_WORKER_ADDRESS_AMOUNT", worker), "1")
		}
		checks = append(checks, check)
	}
	return workerURLs, checks, workerProofCrypto
}

func (b Builder) payoutAmount(crypto string) string {
	key := "FINAL_PLAN_PAYOUT_AMOUNT_" + strings.NewReplacer("-", "_").Replace(strings.ToUpper(crypto))
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	if strings.TrimSpace(b.cfg.PayoutAmount) != "" {
		return b.cfg.PayoutAmount
	}
	return "replace-with-" + slug(crypto) + "-small-value-amount"
}

func enabledWalletCryptos(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT crypto FROM wallet WHERE enabled = 1 ORDER BY crypto")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var crypto string
		if err := rows.Scan(&crypto); err != nil {
			return nil, err
		}
		out = append(out, crypto)
	}
	return normalizeCryptos(out), rows.Err()
}

func Write(path string, plan Plan) error {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if strings.TrimSpace(path) == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o600)
}

func Readiness(plan Plan) ReadinessReport {
	workers := sortedKeys(plan.WorkerURLs)
	secretOverrides := stringSet(plan.SecretOverrideFields)
	placeholderFields := filterFields(planPlaceholderFields(plan), secretOverrides)
	placeholderGroups := fieldGroups(placeholderFields)
	missingFields := filterFields(planMissingFields(plan), secretOverrides)
	missingFieldGroups := fieldGroups(missingFields)
	missingCoverage := coverageGaps(plan)
	missingWorkers := workerGaps(plan, workers)
	warnings := make([]string, 0)
	if strings.TrimSpace(plan.OrderExternalID) == "" {
		warnings = append(warnings, "order_external_id is empty; standalone order lookup will be skipped")
	}
	if plan.Mutating && !plan.RequirePaymentCoverage {
		warnings = append(warnings, "payment coverage gate is disabled")
	}
	if plan.Mutating && !plan.RequirePayoutCoverage {
		warnings = append(warnings, "payout coverage gate is disabled")
	}
	if !plan.OrderStatusMatrixCheck {
		missingFields = append(missingFields, "order_status_matrix_check")
		warnings = append(warnings, "order status matrix check is disabled")
		sort.Strings(missingFields)
	}
	readyForDeployCheck := len(placeholderFields) == 0 && len(missingFields) == 0 && len(missingCoverage) == 0 && len(missingWorkers) == 0
	status := "ready"
	if !readyForDeployCheck {
		status = "blocked"
	}
	nextEnv := nextRequiredEnv(placeholderGroups, missingFieldGroups, len(missingCoverage) > 0, len(missingWorkers) > 0)
	if len(nextEnv) == 0 {
		nextEnv = nil
	}
	return ReadinessReport{
		Status:               status,
		GeneratedAt:          plan.GeneratedAt,
		Source:               plan.Source,
		CoverageCryptos:      append([]string(nil), plan.CoverageCryptos...),
		Workers:              workers,
		PaymentCheckCount:    len(plan.PaymentChecks),
		PayoutCheckCount:     len(plan.PayoutChecks),
		WorkerAddressCount:   len(plan.WorkerAddressChecks),
		Mutating:             plan.Mutating,
		RequirePayment:       plan.RequirePaymentCoverage,
		RequirePayout:        plan.RequirePayoutCoverage,
		OrderStatusMatrix:    plan.OrderStatusMatrixCheck,
		OrderExternalID:      plan.OrderExternalID,
		OrderListCrypto:      plan.OrderListCrypto,
		PlaceholderFields:    placeholderFields,
		PlaceholderGroups:    placeholderGroups,
		MissingFields:        missingFields,
		MissingFieldGroups:   missingFieldGroups,
		MissingCoverage:      missingCoverage,
		MissingWorkers:       missingWorkers,
		Warnings:             warnings,
		NextRequiredEnv:      nextEnv,
		SecretOverrideFields: append([]string(nil), plan.SecretOverrideFields...),
		ReadyForDeployCheck:  readyForDeployCheck,
		ReadyForCutoverAudit: readyForDeployCheck,
		ReadyForPostCutover:  false,
	}
}

func WriteReadiness(path string, report ReadinessReport) error {
	if strings.TrimSpace(path) == "" {
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

func planMissingFields(plan Plan) []string {
	fields := make([]string, 0)
	add := func(name string, value string) {
		if strings.TrimSpace(value) == "" {
			fields = append(fields, name)
		}
	}
	if len(plan.PaymentChecks) > 0 {
		add("api_key", plan.APIKey)
	}
	if len(plan.PayoutChecks) > 0 || plan.Mutating {
		add("admin_username", plan.AdminUsername)
		add("admin_password", plan.AdminPassword)
	}
	if plan.Mutating {
		add("admin_update_username", plan.AdminUpdateUsername)
		add("admin_update_password", plan.AdminUpdatePassword)
	}
	for i, check := range plan.PaymentChecks {
		prefix := fmt.Sprintf("payment_checks[%d].", i)
		add(prefix+"crypto", check.Crypto)
		add(prefix+"amount", check.Amount)
	}
	for i, check := range plan.PayoutChecks {
		prefix := fmt.Sprintf("payout_checks[%d].", i)
		add(prefix+"crypto", check.Crypto)
		add(prefix+"amount", check.Amount)
		if strings.TrimSpace(check.Destination) == "" && strings.TrimSpace(check.DestinationFromPayment) == "" {
			fields = append(fields, prefix+"destination")
		}
	}
	for i, check := range plan.WorkerAddressChecks {
		prefix := fmt.Sprintf("worker_address_checks[%d].", i)
		add(prefix+"worker", check.Worker)
		add(prefix+"crypto", check.Crypto)
		if plan.Mutating {
			add(prefix+"username", check.Username)
			add(prefix+"password", check.Password)
		}
	}
	sort.Strings(fields)
	return fields
}

func planPlaceholderFields(plan Plan) []string {
	fields := make([]string, 0)
	add := func(name string, value string) {
		if hasPlanPlaceholder(value) {
			fields = append(fields, name)
		}
	}
	add("api_key", plan.APIKey)
	add("order_external_id", plan.OrderExternalID)
	add("admin_username", plan.AdminUsername)
	add("admin_password", plan.AdminPassword)
	add("admin_update_username", plan.AdminUpdateUsername)
	add("admin_update_password", plan.AdminUpdatePassword)
	for i, check := range plan.PaymentChecks {
		prefix := fmt.Sprintf("payment_checks[%d].", i)
		add(prefix+"amount", check.Amount)
		add(prefix+"external_id", check.ExternalID)
		add(prefix+"external_id_prefix", check.ExternalIDPrefix)
		add(prefix+"callback_url", check.CallbackURL)
	}
	for i, check := range plan.PayoutChecks {
		prefix := fmt.Sprintf("payout_checks[%d].", i)
		add(prefix+"destination", check.Destination)
		add(prefix+"destination_from_payment", check.DestinationFromPayment)
		add(prefix+"amount", check.Amount)
		add(prefix+"fee", check.Fee)
		add(prefix+"external_id", check.ExternalID)
		add(prefix+"external_id_prefix", check.ExternalIDPrefix)
		add(prefix+"callback_url", check.CallbackURL)
	}
	for i, check := range plan.WorkerAddressChecks {
		prefix := fmt.Sprintf("worker_address_checks[%d].", i)
		add(prefix+"url", check.URL)
		add(prefix+"username", check.Username)
		add(prefix+"password", check.Password)
		add(prefix+"amount", check.Amount)
	}
	return fields
}

func coverageGaps(plan Plan) []string {
	required := map[string]struct{}{}
	for _, crypto := range plan.CoverageCryptos {
		crypto = strings.ToUpper(strings.TrimSpace(crypto))
		if crypto != "" {
			required[crypto] = struct{}{}
		}
	}
	payments := map[string]struct{}{}
	for _, check := range plan.PaymentChecks {
		payments[strings.ToUpper(strings.TrimSpace(check.Crypto))] = struct{}{}
	}
	payouts := map[string]struct{}{}
	for _, check := range plan.PayoutChecks {
		payouts[strings.ToUpper(strings.TrimSpace(check.Crypto))] = struct{}{}
	}
	missing := make([]string, 0)
	for crypto := range required {
		if plan.RequirePaymentCoverage {
			if _, ok := payments[crypto]; !ok {
				missing = append(missing, crypto+":payment")
			}
		}
		if plan.RequirePayoutCoverage {
			if _, ok := payouts[crypto]; !ok {
				missing = append(missing, crypto+":payout")
			}
		}
	}
	sort.Strings(missing)
	return missing
}

func workerGaps(plan Plan, workers []string) []string {
	addressWorkers := map[string]struct{}{}
	for _, check := range plan.WorkerAddressChecks {
		worker := strings.ToLower(strings.TrimSpace(check.Worker))
		if worker != "" {
			addressWorkers[worker] = struct{}{}
		}
	}
	missing := make([]string, 0)
	for _, worker := range workers {
		if _, ok := addressWorkers[worker]; !ok {
			missing = append(missing, worker)
		}
	}
	return missing
}

func fieldGroups(fields []string) map[string][]string {
	if len(fields) == 0 {
		return nil
	}
	groups := map[string][]string{}
	for _, field := range fields {
		group := fieldGroup(field)
		groups[group] = append(groups[group], field)
	}
	for group := range groups {
		sort.Strings(groups[group])
	}
	return groups
}

func filterFields(fields []string, ignored map[string]struct{}) []string {
	if len(fields) == 0 || len(ignored) == 0 {
		return fields
	}
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, ok := ignored[field]; ok {
			continue
		}
		out = append(out, field)
	}
	return out
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func fieldGroup(field string) string {
	switch {
	case field == "api_key":
		return "api_key"
	case strings.HasPrefix(field, "admin_"):
		return "admin_credentials"
	case strings.HasPrefix(field, "worker_address_checks[") && (strings.Contains(field, ".username") || strings.Contains(field, ".password")):
		return "worker_credentials"
	case strings.HasPrefix(field, "payout_checks[") && strings.Contains(field, ".amount"):
		return "payout_amounts"
	case strings.HasPrefix(field, "payment_checks["):
		return "payment_checks"
	case strings.HasPrefix(field, "payout_checks["):
		return "payout_checks"
	case strings.HasPrefix(field, "worker_address_checks["):
		return "worker_address_checks"
	default:
		return "other"
	}
}

func nextRequiredEnv(placeholderGroups map[string][]string, missingGroups map[string][]string, coverageMissing bool, workersMissing bool) map[string]string {
	out := map[string]string{}
	hasGroup := func(name string) bool {
		return len(placeholderGroups[name]) > 0 || len(missingGroups[name]) > 0
	}
	if hasGroup("api_key") {
		out["api_key"] = "Set FINAL_PLAN_API_KEY, FINAL_PLAN_API_KEY_FILE, or use FINAL_PLAN_USE_WALLET_API_KEY=true with a populated MariaDB wallet.apikey."
	}
	if hasGroup("admin_credentials") {
		out["admin_credentials"] = "Set FINAL_PLAN_ADMIN_PASSWORD_FILE and FINAL_PLAN_ADMIN_UPDATE_PASSWORD_FILE, or set the matching FINAL_PLAN_ADMIN_PASSWORD / FINAL_PLAN_ADMIN_UPDATE_PASSWORD env vars."
	}
	if hasGroup("worker_credentials") {
		out["worker_credentials"] = "Set FINAL_PLAN_WORKER_USERNAME and FINAL_PLAN_WORKER_PASSWORD_FILE, set per-worker FINAL_PLAN_WORKER_USERNAME_<WORKER>/FINAL_PLAN_WORKER_PASSWORD_<WORKER>_FILE, or use FINAL_PLAN_USE_WALLET_SERVERKEY=true after saving wallet.serverkey as username:password."
	}
	if hasGroup("payout_amounts") {
		out["payout_amounts"] = "Set FINAL_PLAN_PAYOUT_AMOUNT for a shared small-value rehearsal amount, or set FINAL_PLAN_PAYOUT_AMOUNT_<CRYPTO> for every payout check."
	}
	if coverageMissing {
		out["coverage"] = "Add payment_checks and payout_checks for every coverage_cryptos entry, or regenerate the plan with final-plan."
	}
	if workersMissing {
		out["workers"] = "Add worker_address_checks for every worker URL in the plan."
	}
	if hasGroup("payment_checks") || hasGroup("payout_checks") || hasGroup("worker_address_checks") || hasGroup("other") {
		out["plan_fields"] = "Fill the remaining placeholder or missing plan fields before running mutating deploy-check."
	}
	return out
}

func sortedKeys(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		key = strings.TrimSpace(key)
		if key != "" {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func hasPlanPlaceholder(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lowered := strings.ToLower(value)
	return strings.Contains(lowered, "replace-with") ||
		strings.Contains(lowered, "changeme") ||
		strings.Contains(lowered, "todo") ||
		strings.Contains(value, "${") ||
		strings.Contains(value, "{{")
}

func allDefaultCryptos(defs []app.CryptoModule) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return normalizeCryptos(out)
}

func normalizeCryptos(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
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

func workerName(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	switch host {
	case "btc-lightning-worker":
		return "lightning"
	default:
		return strings.TrimSuffix(host, "-worker")
	}
}

func defaultPort(def app.CryptoModule) string {
	if strings.TrimSpace(def.DefaultPort) != "" {
		return strings.TrimSpace(def.DefaultPort)
	}
	return "6000"
}

func nativeCrypto(def app.CryptoModule) string {
	switch def.Network {
	case "TRX":
		return "TRX"
	case "MATIC":
		return "MATIC"
	case "AVAX":
		return "AVAX"
	case "ARBETH":
		return "ARBETH"
	case "OPETH":
		return "OPETH"
	case "SOL":
		return "SOL"
	case "BTC":
		if def.Name == "BTC-LIGHTNING" {
			return "BTC-LIGHTNING"
		}
		return def.Name
	default:
		if def.Name == "BNB" || strings.HasPrefix(def.Name, "BNB-") {
			return "BNB"
		}
		if def.Name == "ETH" || strings.HasPrefix(def.Name, "ETH-") {
			return "ETH"
		}
		return def.Name
	}
}

func parseDatabaseURLCompat(raw string) (string, string) {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err == nil {
			user := u.User.Username()
			pass, _ := u.User.Password()
			host := u.Host
			dbname := strings.TrimPrefix(u.Path, "/")
			query := u.Query()
			if query.Get("parseTime") == "" {
				query.Set("parseTime", "true")
			}
			if query.Get("charset") == "" {
				query.Set("charset", "utf8mb4")
			}
			if query.Get("loc") == "" {
				query.Set("loc", "Local")
			}
			return "mysql", user + ":" + pass + "@tcp(" + host + ")/" + dbname + "?" + query.Encode()
		}
	}
	return "mysql", raw
}

func databaseURLConfigError(raw string) error {
	value := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(value, "sqlite:") || strings.Contains(value, "sqlite://") || strings.Contains(value, ".sqlite") || strings.Contains(value, "sqlite3") {
		return errors.New("SQLite is not supported by final-plan; set MARIADB_DATABASE_URL to a MariaDB/MySQL DSN")
	}
	if strings.HasPrefix(value, "mariadb://") || strings.HasPrefix(value, "mysql://") || strings.Contains(value, "@tcp(") {
		return nil
	}
	return errors.New("MARIADB_DATABASE_URL must be a MariaDB/MySQL DSN, for example mariadb://user:password@mariadb:3306/shkeeper")
}

func workerEnv(prefix string, worker string) string {
	return prefix + "_" + strings.NewReplacer("-", "_").Replace(strings.ToUpper(worker))
}

func workerCredential(prefix string, worker string, fallback string) string {
	if value := secretEnv(workerEnv(prefix, worker), ""); value != "" {
		return value
	}
	if value := secretEnv(prefix, ""); value != "" {
		return value
	}
	return fallback
}

func slug(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", "-"))
}

func positive(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func env(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func secretEnv(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	fileKey := key + "_FILE"
	if path := strings.TrimSpace(os.Getenv(fileKey)); path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			if value := strings.TrimSpace(string(data)); value != "" {
				return value
			}
		}
	}
	return fallback
}

func secretFileEnvSet(key string) bool {
	return strings.TrimSpace(os.Getenv(key+"_FILE")) != ""
}

func workerSecretFileEnvSet(prefix string, worker string) bool {
	return secretFileEnvSet(workerEnv(prefix, worker)) || secretFileEnvSet(prefix)
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyEnv(keysAndFallback ...string) string {
	if len(keysAndFallback) == 0 {
		return ""
	}
	fallback := keysAndFallback[len(keysAndFallback)-1]
	for _, key := range keysAndFallback[:len(keysAndFallback)-1] {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return fallback
}

func firstNonEmptySecretEnv(keysAndFallback ...string) string {
	if len(keysAndFallback) == 0 {
		return ""
	}
	fallback := keysAndFallback[len(keysAndFallback)-1]
	for _, key := range keysAndFallback[:len(keysAndFallback)-1] {
		if value := secretEnv(key, ""); value != "" {
			return value
		}
	}
	return fallback
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
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func intEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	var out int
	if _, err := fmt.Sscanf(value, "%d", &out); err != nil || out <= 0 {
		return fallback
	}
	return out
}
