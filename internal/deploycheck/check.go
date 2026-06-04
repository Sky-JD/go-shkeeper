package deploycheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	MainURL                string
	WorkerURL              string
	APIKey                 string
	AdminUsername          string
	AdminPassword          string
	AdminUpdateUsername    string
	AdminUpdatePassword    string
	WorkerUsername         string
	WorkerPassword         string
	Crypto                 string
	Cryptos                []string
	CoverageCryptos        []string
	WorkerURLs             []NamedURL
	OrderExternalID        string
	ExpectStatus           string
	OrderRequests          int
	OrderConcurrency       int
	OrderMaxLatency        time.Duration
	OrderListCheck         bool
	OrderListStatus        string
	OrderListCrypto        string
	OrderListLimit         int
	OrderListMinResults    int
	OrderListRequests      int
	OrderListConcurrency   int
	OrderListMaxLatency    time.Duration
	OrderStatusMatrixCheck bool
	OrderStatusMatrixLimit int
	OrderStatusMatrixPages int
	OrderStatusMatrixMin   int
	OrderStatusMatrixWant  []string
	ExpectServerStatus     string
	MainStatusCheck        bool
	WorkerTaskID           string
	WorkerStatusCheck      bool
	Mutating               bool
	RequirePaymentCoverage bool
	RequirePayoutCoverage  bool
	PaymentChecks          []PaymentCheck
	PayoutChecks           []PayoutCheck
	WorkerAddressChecks    []WorkerAddressCheck
	ReportFile             string
	Timeout                time.Duration
	Concurrency            int
	Requests               int
}

type Runner struct {
	cfg            Config
	client         *http.Client
	out            io.Writer
	reportMu       sync.Mutex
	startedAt      time.Time
	finishedAt     time.Time
	checks         []ReportCheck
	paymentWallets map[string]string
}

type NamedURL struct {
	Name string
	URL  string
}

type PaymentCheck struct {
	Name             string `json:"name"`
	Crypto           string `json:"crypto"`
	Fiat             string `json:"fiat"`
	Amount           string `json:"amount"`
	ExternalID       string `json:"external_id"`
	ExternalIDPrefix string `json:"external_id_prefix"`
	CallbackURL      string `json:"callback_url"`
	ExpectStatus     string `json:"expect_status"`
}

type PayoutCheck struct {
	Name                   string `json:"name"`
	Crypto                 string `json:"crypto"`
	Destination            string `json:"destination"`
	DestinationFromPayment string `json:"destination_from_payment"`
	Amount                 string `json:"amount"`
	Fee                    string `json:"fee"`
	ExternalID             string `json:"external_id"`
	ExternalIDPrefix       string `json:"external_id_prefix"`
	CallbackURL            string `json:"callback_url"`
	ExpectStatus           string `json:"expect_status"`
}

type WorkerAddressCheck struct {
	Name     string `json:"name"`
	Worker   string `json:"worker"`
	URL      string `json:"url"`
	Crypto   string `json:"crypto"`
	Username string `json:"username"`
	Password string `json:"password"`
	Amount   string `json:"amount"`
}

type Report struct {
	Status          string        `json:"status"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      time.Time     `json:"finished_at"`
	DurationMS      int64         `json:"duration_ms"`
	MainURL         string        `json:"main_url,omitempty"`
	Cryptos         []string      `json:"cryptos,omitempty"`
	CoverageCryptos []string      `json:"coverage_cryptos,omitempty"`
	Mutating        bool          `json:"mutating"`
	Error           string        `json:"error,omitempty"`
	Checks          []ReportCheck `json:"checks"`
}

type ReportCheck struct {
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Details   map[string]string `json:"details,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Error     string            `json:"error,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

type planConfig struct {
	MainURL                string               `json:"main_url"`
	WorkerURL              string               `json:"worker_url"`
	WorkerURLs             json.RawMessage      `json:"worker_urls"`
	APIKey                 string               `json:"api_key"`
	AdminUsername          string               `json:"admin_username"`
	AdminPassword          string               `json:"admin_password"`
	AdminUpdateUsername    string               `json:"admin_update_username"`
	AdminUpdatePassword    string               `json:"admin_update_password"`
	WorkerUsername         string               `json:"worker_username"`
	WorkerPassword         string               `json:"worker_password"`
	Crypto                 string               `json:"crypto"`
	Cryptos                []string             `json:"cryptos"`
	CoverageCryptos        []string             `json:"coverage_cryptos"`
	OrderExternalID        string               `json:"order_external_id"`
	ExpectStatus           string               `json:"expect_status"`
	OrderRequests          *int                 `json:"order_requests"`
	OrderConcurrency       *int                 `json:"order_concurrency"`
	OrderMaxLatencyMS      *int                 `json:"order_max_latency_ms"`
	OrderListCheck         *bool                `json:"order_list_check"`
	OrderListStatus        string               `json:"order_list_status"`
	OrderListCrypto        string               `json:"order_list_crypto"`
	OrderListLimit         *int                 `json:"order_list_limit"`
	OrderListMinResults    *int                 `json:"order_list_min_results"`
	OrderListRequests      *int                 `json:"order_list_requests"`
	OrderListConcurrency   *int                 `json:"order_list_concurrency"`
	OrderListMaxLatencyMS  *int                 `json:"order_list_max_latency_ms"`
	OrderStatusMatrixCheck *bool                `json:"order_status_matrix_check"`
	OrderStatusMatrix      *bool                `json:"order_status_matrix"`
	OrderStatusMatrixLimit *int                 `json:"order_status_matrix_limit"`
	OrderStatusMatrixPages *int                 `json:"order_status_matrix_pages"`
	OrderStatusMatrixMin   *int                 `json:"order_status_matrix_min"`
	OrderStatusMatrixWant  []string             `json:"order_status_matrix_expect_statuses"`
	ExpectServerStatus     string               `json:"expect_server_status"`
	MainStatusCheck        *bool                `json:"main_status_check"`
	MainStatus             *bool                `json:"main_status"`
	WorkerTaskID           string               `json:"worker_task_id"`
	WorkerStatusCheck      *bool                `json:"worker_status_check"`
	WorkerStatus           *bool                `json:"worker_status"`
	Mutating               *bool                `json:"mutating"`
	RequirePaymentCoverage *bool                `json:"require_payment_coverage"`
	RequirePayoutCoverage  *bool                `json:"require_payout_coverage"`
	PaymentChecks          []PaymentCheck       `json:"payment_checks"`
	PayoutChecks           []PayoutCheck        `json:"payout_checks"`
	WorkerAddressChecks    []WorkerAddressCheck `json:"worker_address_checks"`
	ReportFile             string               `json:"report_file"`
	TimeoutSeconds         *int                 `json:"timeout_seconds"`
	Concurrency            *int                 `json:"concurrency"`
	Requests               *int                 `json:"requests"`
	Source                 string               `json:"source"`
	GeneratedAt            string               `json:"generated_at"`
	Notes                  []string             `json:"notes"`
	WorkerCryptoMap        map[string]string    `json:"worker_crypto_map"`
	Placeholders           map[string]string    `json:"placeholders"`
	SecretOverrideFields   []string             `json:"secret_override_fields"`
}

type planNamedURL struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func LoadConfig() (Config, error) {
	cfg := defaultConfig()
	if path := strings.TrimSpace(os.Getenv("DEPLOY_CHECK_PLAN_FILE")); path != "" {
		if err := applyPlanFile(&cfg, path); err != nil {
			return cfg, err
		}
	}
	applyEnvOverrides(&cfg)
	normalizeConfig(&cfg)
	return cfg, nil
}

func LoadConfigFromEnv() Config {
	cfg := defaultConfig()
	applyEnvOverrides(&cfg)
	normalizeConfig(&cfg)
	return cfg
}

func defaultConfig() Config {
	return Config{
		MainURL:                "http://127.0.0.1:5000",
		Crypto:                 "BTC",
		Cryptos:                []string{"BTC"},
		OrderRequests:          1,
		OrderConcurrency:       1,
		OrderListLimit:         50,
		OrderListMinResults:    1,
		OrderListRequests:      1,
		OrderListConcurrency:   1,
		OrderStatusMatrixLimit: 100,
		OrderStatusMatrixPages: 20,
		Timeout:                10 * time.Second,
		Concurrency:            1,
		Requests:               1,
	}
}

func applyPlanFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read deploy-check plan file %s: %w", path, err)
	}
	var plan planConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return fmt.Errorf("decode deploy-check plan file %s: %w", path, err)
	}
	if err := applyPlanConfig(cfg, plan); err != nil {
		return fmt.Errorf("apply deploy-check plan file %s: %w", path, err)
	}
	return nil
}

func applyPlanConfig(cfg *Config, plan planConfig) error {
	setString(&cfg.MainURL, plan.MainURL)
	setString(&cfg.WorkerURL, plan.WorkerURL)
	setString(&cfg.APIKey, plan.APIKey)
	setString(&cfg.AdminUsername, plan.AdminUsername)
	setString(&cfg.AdminPassword, plan.AdminPassword)
	setString(&cfg.AdminUpdateUsername, plan.AdminUpdateUsername)
	setString(&cfg.AdminUpdatePassword, plan.AdminUpdatePassword)
	setString(&cfg.WorkerUsername, plan.WorkerUsername)
	setString(&cfg.WorkerPassword, plan.WorkerPassword)
	if strings.TrimSpace(plan.Crypto) != "" {
		cfg.Crypto = strings.ToUpper(strings.TrimSpace(plan.Crypto))
	}
	if len(plan.Cryptos) > 0 {
		cfg.Cryptos = normalizeCryptos(plan.Cryptos)
	}
	if len(plan.CoverageCryptos) > 0 {
		cfg.CoverageCryptos = normalizeCryptos(plan.CoverageCryptos)
	}
	if len(bytes.TrimSpace(plan.WorkerURLs)) > 0 {
		workers, err := parsePlanWorkerURLs(plan.WorkerURLs)
		if err != nil {
			return err
		}
		cfg.WorkerURLs = workers
	}
	setString(&cfg.OrderExternalID, plan.OrderExternalID)
	if strings.TrimSpace(plan.ExpectStatus) != "" {
		cfg.ExpectStatus = strings.ToUpper(strings.TrimSpace(plan.ExpectStatus))
	}
	setInt(&cfg.OrderRequests, plan.OrderRequests)
	setInt(&cfg.OrderConcurrency, plan.OrderConcurrency)
	if plan.OrderMaxLatencyMS != nil {
		cfg.OrderMaxLatency = time.Duration(*plan.OrderMaxLatencyMS) * time.Millisecond
	}
	if plan.OrderListCheck != nil {
		cfg.OrderListCheck = *plan.OrderListCheck
	}
	setString(&cfg.OrderListStatus, plan.OrderListStatus)
	setString(&cfg.OrderListCrypto, plan.OrderListCrypto)
	setInt(&cfg.OrderListLimit, plan.OrderListLimit)
	setInt(&cfg.OrderListMinResults, plan.OrderListMinResults)
	setInt(&cfg.OrderListRequests, plan.OrderListRequests)
	setInt(&cfg.OrderListConcurrency, plan.OrderListConcurrency)
	if plan.OrderListMaxLatencyMS != nil {
		cfg.OrderListMaxLatency = time.Duration(*plan.OrderListMaxLatencyMS) * time.Millisecond
	}
	if plan.OrderStatusMatrixCheck != nil {
		cfg.OrderStatusMatrixCheck = *plan.OrderStatusMatrixCheck
	}
	if plan.OrderStatusMatrix != nil {
		cfg.OrderStatusMatrixCheck = *plan.OrderStatusMatrix
	}
	setInt(&cfg.OrderStatusMatrixLimit, plan.OrderStatusMatrixLimit)
	setInt(&cfg.OrderStatusMatrixPages, plan.OrderStatusMatrixPages)
	setInt(&cfg.OrderStatusMatrixMin, plan.OrderStatusMatrixMin)
	if len(plan.OrderStatusMatrixWant) > 0 {
		cfg.OrderStatusMatrixWant = normalizeStatuses(plan.OrderStatusMatrixWant)
	}
	setString(&cfg.ExpectServerStatus, plan.ExpectServerStatus)
	if plan.MainStatusCheck != nil {
		cfg.MainStatusCheck = *plan.MainStatusCheck
	}
	if plan.MainStatus != nil {
		cfg.MainStatusCheck = *plan.MainStatus
	}
	setString(&cfg.WorkerTaskID, plan.WorkerTaskID)
	if plan.WorkerStatusCheck != nil {
		cfg.WorkerStatusCheck = *plan.WorkerStatusCheck
	}
	if plan.WorkerStatus != nil {
		cfg.WorkerStatusCheck = *plan.WorkerStatus
	}
	if plan.Mutating != nil {
		cfg.Mutating = *plan.Mutating
	}
	if plan.RequirePaymentCoverage != nil {
		cfg.RequirePaymentCoverage = *plan.RequirePaymentCoverage
	}
	if plan.RequirePayoutCoverage != nil {
		cfg.RequirePayoutCoverage = *plan.RequirePayoutCoverage
	}
	if len(plan.PaymentChecks) > 0 {
		cfg.PaymentChecks = plan.PaymentChecks
	}
	if len(plan.PayoutChecks) > 0 {
		cfg.PayoutChecks = plan.PayoutChecks
	}
	if len(plan.WorkerAddressChecks) > 0 {
		cfg.WorkerAddressChecks = plan.WorkerAddressChecks
	}
	setString(&cfg.ReportFile, plan.ReportFile)
	if plan.TimeoutSeconds != nil {
		cfg.Timeout = time.Duration(secondsValue(*plan.TimeoutSeconds, 10)) * time.Second
	}
	setInt(&cfg.Concurrency, plan.Concurrency)
	setInt(&cfg.Requests, plan.Requests)
	return nil
}

func parsePlanWorkerURLs(raw json.RawMessage) ([]NamedURL, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	switch trimmed[0] {
	case '"':
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, err
		}
		return parseNamedURLs(value), nil
	case '{':
		var values map[string]string
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return nil, err
		}
		out := make([]NamedURL, 0, len(values))
		for name, urlValue := range values {
			name = strings.TrimSpace(name)
			urlValue = strings.TrimSpace(urlValue)
			if name == "" || urlValue == "" {
				continue
			}
			out = append(out, NamedURL{Name: name, URL: urlValue})
		}
		return out, nil
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		out := make([]NamedURL, 0, len(items))
		for _, item := range items {
			item = bytes.TrimSpace(item)
			if len(item) == 0 || bytes.Equal(item, []byte("null")) {
				continue
			}
			if item[0] == '"' {
				var value string
				if err := json.Unmarshal(item, &value); err != nil {
					return nil, err
				}
				out = append(out, parseNamedURLs(value)...)
				continue
			}
			var named planNamedURL
			if err := json.Unmarshal(item, &named); err != nil {
				return nil, err
			}
			if strings.TrimSpace(named.URL) == "" {
				continue
			}
			name := strings.TrimSpace(named.Name)
			if name == "" {
				name = fmt.Sprintf("worker%d", len(out)+1)
			}
			out = append(out, NamedURL{Name: name, URL: strings.TrimSpace(named.URL)})
		}
		return out, nil
	default:
		return nil, errors.New("worker_urls must be a string, object map, or array")
	}
}

func applyEnvOverrides(cfg *Config) {
	if value, ok := envValue("DEPLOY_CHECK_MAIN_URL", "MAIN_URL"); ok {
		cfg.MainURL = value
	}
	if value, ok := envValue("DEPLOY_CHECK_WORKER_URL", "WORKER_URL"); ok {
		cfg.WorkerURL = value
	}
	if value, ok := secretEnvValue("DEPLOY_CHECK_API_KEY", "API_KEY"); ok {
		cfg.APIKey = value
	}
	if value, ok := envValue("DEPLOY_CHECK_ADMIN_USERNAME", "ADMIN_USERNAME"); ok {
		cfg.AdminUsername = value
	}
	if value, ok := secretEnvValue("DEPLOY_CHECK_ADMIN_PASSWORD", "ADMIN_PASSWORD"); ok {
		cfg.AdminPassword = value
	}
	if value, ok := envValue("DEPLOY_CHECK_ADMIN_UPDATE_USERNAME", "ADMIN_UPDATE_USERNAME"); ok {
		cfg.AdminUpdateUsername = value
	}
	if value, ok := secretEnvValue("DEPLOY_CHECK_ADMIN_UPDATE_PASSWORD", "ADMIN_UPDATE_PASSWORD"); ok {
		cfg.AdminUpdatePassword = value
	}
	if value, ok := envValue("DEPLOY_CHECK_WORKER_USERNAME", "WORKER_USERNAME"); ok {
		cfg.WorkerUsername = value
	}
	if value, ok := secretEnvValue("DEPLOY_CHECK_WORKER_PASSWORD", "WORKER_PASSWORD"); ok {
		cfg.WorkerPassword = value
	}
	if value, ok := envValue("DEPLOY_CHECK_WORKER_URLS", "WORKER_URLS"); ok {
		cfg.WorkerURLs = parseNamedURLs(value)
	}
	if value, ok := envValue("DEPLOY_CHECK_CRYPTO", "CRYPTO"); ok {
		cfg.Crypto = strings.ToUpper(strings.TrimSpace(value))
	}
	if value, ok := envValue("DEPLOY_CHECK_CRYPTOS", "CRYPTOS"); ok {
		cfg.Cryptos = splitCSVUpper(value)
		cfg.MainStatusCheck = true
	}
	if value, ok := envValue("DEPLOY_CHECK_COVERAGE_CRYPTOS", "COVERAGE_CRYPTOS"); ok {
		cfg.CoverageCryptos = splitCSVUpper(value)
	}
	if value, ok := envValue("DEPLOY_CHECK_ORDER_EXTERNAL_ID", "ORDER_EXTERNAL_ID"); ok {
		cfg.OrderExternalID = value
	}
	if value, ok := envValue("DEPLOY_CHECK_EXPECT_STATUS", "EXPECT_STATUS"); ok {
		cfg.ExpectStatus = strings.ToUpper(strings.TrimSpace(value))
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_REQUESTS"); ok {
		cfg.OrderRequests = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_CONCURRENCY"); ok {
		cfg.OrderConcurrency = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_MAX_LATENCY_MS"); ok {
		cfg.OrderMaxLatency = time.Duration(value) * time.Millisecond
	}
	if value, ok := boolEnvValue("DEPLOY_CHECK_ORDER_LIST"); ok {
		cfg.OrderListCheck = value
	}
	if value, ok := envValue("DEPLOY_CHECK_ORDER_LIST_STATUS", "ORDER_LIST_STATUS"); ok {
		cfg.OrderListStatus = strings.ToUpper(strings.TrimSpace(value))
	}
	if value, ok := envValue("DEPLOY_CHECK_ORDER_LIST_CRYPTO", "ORDER_LIST_CRYPTO"); ok {
		cfg.OrderListCrypto = strings.ToUpper(strings.TrimSpace(value))
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_LIST_LIMIT"); ok {
		cfg.OrderListLimit = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_LIST_MIN_RESULTS"); ok {
		cfg.OrderListMinResults = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_LIST_REQUESTS"); ok {
		cfg.OrderListRequests = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_LIST_CONCURRENCY"); ok {
		cfg.OrderListConcurrency = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_LIST_MAX_LATENCY_MS"); ok {
		cfg.OrderListMaxLatency = time.Duration(value) * time.Millisecond
	}
	if value, ok := boolEnvValue("DEPLOY_CHECK_ORDER_STATUS_MATRIX"); ok {
		cfg.OrderStatusMatrixCheck = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_STATUS_MATRIX_LIMIT"); ok {
		cfg.OrderStatusMatrixLimit = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_STATUS_MATRIX_PAGES"); ok {
		cfg.OrderStatusMatrixPages = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_ORDER_STATUS_MATRIX_MIN"); ok {
		cfg.OrderStatusMatrixMin = value
	}
	if value, ok := envValue("DEPLOY_CHECK_ORDER_STATUS_MATRIX_EXPECT_STATUSES"); ok {
		cfg.OrderStatusMatrixWant = splitCSVUpper(value)
	}
	if value, ok := envValue("DEPLOY_CHECK_EXPECT_SERVER_STATUS", "EXPECT_SERVER_STATUS"); ok {
		cfg.ExpectServerStatus = value
	}
	if value, ok := boolEnvValue("DEPLOY_CHECK_MAIN_STATUS"); ok {
		cfg.MainStatusCheck = value
	}
	if value, ok := envValue("DEPLOY_CHECK_WORKER_TASK_ID", "WORKER_TASK_ID"); ok {
		cfg.WorkerTaskID = value
	}
	if value, ok := boolEnvValue("DEPLOY_CHECK_WORKER_STATUS"); ok {
		cfg.WorkerStatusCheck = value
	}
	if value, ok := boolEnvValue("DEPLOY_CHECK_MUTATING"); ok {
		cfg.Mutating = value
	}
	if value, ok := boolEnvValue("DEPLOY_CHECK_REQUIRE_PAYMENT_COVERAGE"); ok {
		cfg.RequirePaymentCoverage = value
	}
	if value, ok := boolEnvValue("DEPLOY_CHECK_REQUIRE_PAYOUT_COVERAGE"); ok {
		cfg.RequirePayoutCoverage = value
	}
	if value, ok := envValue("DEPLOY_CHECK_REPORT_FILE"); ok {
		cfg.ReportFile = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_TIMEOUT_SECONDS"); ok {
		cfg.Timeout = time.Duration(secondsValue(value, 10)) * time.Second
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_CONCURRENCY"); ok {
		cfg.Concurrency = value
	}
	if value, ok := intEnvValue("DEPLOY_CHECK_REQUESTS"); ok {
		cfg.Requests = value
	}
}

func normalizeConfig(cfg *Config) {
	cfg.MainURL = strings.TrimSpace(cfg.MainURL)
	cfg.WorkerURL = strings.TrimSpace(cfg.WorkerURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.AdminUsername = strings.TrimSpace(cfg.AdminUsername)
	cfg.AdminPassword = strings.TrimSpace(cfg.AdminPassword)
	cfg.AdminUpdateUsername = strings.TrimSpace(cfg.AdminUpdateUsername)
	cfg.AdminUpdatePassword = strings.TrimSpace(cfg.AdminUpdatePassword)
	cfg.WorkerUsername = strings.TrimSpace(cfg.WorkerUsername)
	cfg.WorkerPassword = strings.TrimSpace(cfg.WorkerPassword)
	cfg.Crypto = strings.ToUpper(strings.TrimSpace(cfg.Crypto))
	cfg.Cryptos = normalizeCryptos(cfg.Cryptos)
	cfg.CoverageCryptos = normalizeCryptos(cfg.CoverageCryptos)
	cfg.OrderExternalID = strings.TrimSpace(cfg.OrderExternalID)
	cfg.ExpectStatus = strings.ToUpper(strings.TrimSpace(cfg.ExpectStatus))
	cfg.OrderListStatus = strings.ToUpper(strings.TrimSpace(cfg.OrderListStatus))
	cfg.OrderListCrypto = strings.ToUpper(strings.TrimSpace(cfg.OrderListCrypto))
	cfg.OrderStatusMatrixWant = normalizeStatuses(cfg.OrderStatusMatrixWant)
	cfg.ExpectServerStatus = strings.TrimSpace(cfg.ExpectServerStatus)
	cfg.WorkerTaskID = strings.TrimSpace(cfg.WorkerTaskID)
	cfg.ReportFile = strings.TrimSpace(cfg.ReportFile)
	if cfg.MainURL == "" {
		cfg.MainURL = "http://127.0.0.1:5000"
	}
	if cfg.Crypto == "" {
		cfg.Crypto = "BTC"
	}
	cfg.Cryptos = normalizeCryptos(cfg.Cryptos)
	cfg.CoverageCryptos = normalizeCryptos(cfg.CoverageCryptos)
	if len(cfg.Cryptos) == 0 {
		cfg.Cryptos = []string{cfg.Crypto}
	}
	if len(cfg.CoverageCryptos) == 0 {
		cfg.CoverageCryptos = append([]string(nil), cfg.Cryptos...)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.OrderRequests <= 0 {
		cfg.OrderRequests = 1
	}
	if cfg.OrderConcurrency <= 0 {
		cfg.OrderConcurrency = 1
	}
	if cfg.OrderListLimit <= 0 {
		cfg.OrderListLimit = 50
	}
	if cfg.OrderStatusMatrixLimit <= 0 {
		cfg.OrderStatusMatrixLimit = 100
	}
	if cfg.OrderStatusMatrixPages <= 0 {
		cfg.OrderStatusMatrixPages = 20
	}
	if cfg.OrderListMinResults <= 0 {
		cfg.OrderListMinResults = 1
	}
	if cfg.OrderListRequests <= 0 {
		cfg.OrderListRequests = 1
	}
	if cfg.OrderListConcurrency <= 0 {
		cfg.OrderListConcurrency = 1
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.Requests <= 0 {
		cfg.Requests = 1
	}
	for i := range cfg.PaymentChecks {
		cfg.PaymentChecks[i].Name = strings.TrimSpace(cfg.PaymentChecks[i].Name)
		cfg.PaymentChecks[i].Crypto = strings.ToUpper(strings.TrimSpace(cfg.PaymentChecks[i].Crypto))
		if cfg.PaymentChecks[i].Crypto == "" {
			cfg.PaymentChecks[i].Crypto = cfg.Crypto
		}
		cfg.PaymentChecks[i].Fiat = strings.ToUpper(strings.TrimSpace(cfg.PaymentChecks[i].Fiat))
		if cfg.PaymentChecks[i].Fiat == "" {
			cfg.PaymentChecks[i].Fiat = "USD"
		}
		cfg.PaymentChecks[i].Amount = strings.TrimSpace(cfg.PaymentChecks[i].Amount)
		if hasPlanPlaceholder(cfg.PaymentChecks[i].Amount) || cfg.PaymentChecks[i].Amount == "" {
			cfg.PaymentChecks[i].Amount = checkAmountFromEnv("DEPLOY_CHECK_PAYMENT_AMOUNT", cfg.PaymentChecks[i].Crypto, cfg.PaymentChecks[i].Amount)
		}
		cfg.PaymentChecks[i].ExternalID = strings.TrimSpace(cfg.PaymentChecks[i].ExternalID)
		cfg.PaymentChecks[i].ExternalIDPrefix = strings.TrimSpace(cfg.PaymentChecks[i].ExternalIDPrefix)
		cfg.PaymentChecks[i].CallbackURL = strings.TrimSpace(cfg.PaymentChecks[i].CallbackURL)
		cfg.PaymentChecks[i].ExpectStatus = strings.ToUpper(strings.TrimSpace(cfg.PaymentChecks[i].ExpectStatus))
		if cfg.PaymentChecks[i].ExpectStatus == "" {
			cfg.PaymentChecks[i].ExpectStatus = "UNPAID"
		}
	}
	for i := range cfg.PayoutChecks {
		cfg.PayoutChecks[i].Name = strings.TrimSpace(cfg.PayoutChecks[i].Name)
		cfg.PayoutChecks[i].Crypto = strings.ToUpper(strings.TrimSpace(cfg.PayoutChecks[i].Crypto))
		if cfg.PayoutChecks[i].Crypto == "" {
			cfg.PayoutChecks[i].Crypto = cfg.Crypto
		}
		cfg.PayoutChecks[i].Destination = strings.TrimSpace(cfg.PayoutChecks[i].Destination)
		cfg.PayoutChecks[i].DestinationFromPayment = strings.TrimSpace(cfg.PayoutChecks[i].DestinationFromPayment)
		cfg.PayoutChecks[i].Amount = strings.TrimSpace(cfg.PayoutChecks[i].Amount)
		if hasPlanPlaceholder(cfg.PayoutChecks[i].Amount) || cfg.PayoutChecks[i].Amount == "" {
			cfg.PayoutChecks[i].Amount = checkAmountFromEnv("DEPLOY_CHECK_PAYOUT_AMOUNT", cfg.PayoutChecks[i].Crypto, cfg.PayoutChecks[i].Amount)
		}
		cfg.PayoutChecks[i].Fee = strings.TrimSpace(cfg.PayoutChecks[i].Fee)
		cfg.PayoutChecks[i].ExternalID = strings.TrimSpace(cfg.PayoutChecks[i].ExternalID)
		cfg.PayoutChecks[i].ExternalIDPrefix = strings.TrimSpace(cfg.PayoutChecks[i].ExternalIDPrefix)
		cfg.PayoutChecks[i].CallbackURL = strings.TrimSpace(cfg.PayoutChecks[i].CallbackURL)
		cfg.PayoutChecks[i].ExpectStatus = strings.ToUpper(strings.TrimSpace(cfg.PayoutChecks[i].ExpectStatus))
	}
	for i := range cfg.WorkerAddressChecks {
		cfg.WorkerAddressChecks[i].Name = strings.TrimSpace(cfg.WorkerAddressChecks[i].Name)
		cfg.WorkerAddressChecks[i].Worker = strings.TrimSpace(cfg.WorkerAddressChecks[i].Worker)
		cfg.WorkerAddressChecks[i].URL = strings.TrimSpace(cfg.WorkerAddressChecks[i].URL)
		cfg.WorkerAddressChecks[i].Crypto = strings.ToUpper(strings.TrimSpace(cfg.WorkerAddressChecks[i].Crypto))
		if cfg.WorkerAddressChecks[i].Crypto == "" {
			cfg.WorkerAddressChecks[i].Crypto = cfg.Crypto
		}
		cfg.WorkerAddressChecks[i].Username = strings.TrimSpace(cfg.WorkerAddressChecks[i].Username)
		if hasPlanPlaceholder(cfg.WorkerAddressChecks[i].Username) || cfg.WorkerAddressChecks[i].Username == "" {
			cfg.WorkerAddressChecks[i].Username = workerValueFromEnv("DEPLOY_CHECK_WORKER_USERNAME", cfg.WorkerAddressChecks[i].Worker, cfg.WorkerUsername, cfg.WorkerAddressChecks[i].Username)
		}
		cfg.WorkerAddressChecks[i].Password = strings.TrimSpace(cfg.WorkerAddressChecks[i].Password)
		if hasPlanPlaceholder(cfg.WorkerAddressChecks[i].Password) || cfg.WorkerAddressChecks[i].Password == "" {
			cfg.WorkerAddressChecks[i].Password = workerSecretFromEnv("DEPLOY_CHECK_WORKER_PASSWORD", cfg.WorkerAddressChecks[i].Worker, cfg.WorkerPassword, cfg.WorkerAddressChecks[i].Password)
		}
		cfg.WorkerAddressChecks[i].Amount = strings.TrimSpace(cfg.WorkerAddressChecks[i].Amount)
	}
	workers := make([]NamedURL, 0, len(cfg.WorkerURLs))
	for _, worker := range cfg.WorkerURLs {
		worker.Name = strings.TrimSpace(worker.Name)
		worker.URL = strings.TrimSpace(worker.URL)
		if worker.URL == "" {
			continue
		}
		if worker.Name == "" {
			worker.Name = fmt.Sprintf("worker%d", len(workers)+1)
		}
		workers = append(workers, worker)
	}
	cfg.WorkerURLs = workers
}

func setString(target *string, value string) {
	if strings.TrimSpace(value) != "" {
		*target = strings.TrimSpace(value)
	}
}

func setInt(target *int, value *int) {
	if value != nil {
		*target = *value
	}
}

func normalizeCryptos(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		item := strings.ToUpper(strings.TrimSpace(value))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func normalizeStatuses(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		item := strings.ToUpper(strings.TrimSpace(value))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func paymentCheckCryptos(checks []PaymentCheck, fallback string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, check := range checks {
		crypto := strings.ToUpper(strings.TrimSpace(check.Crypto))
		if crypto == "" {
			crypto = strings.ToUpper(strings.TrimSpace(fallback))
		}
		if crypto != "" {
			out[crypto] = struct{}{}
		}
	}
	return out
}

func payoutCheckCryptos(checks []PayoutCheck, fallback string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, check := range checks {
		crypto := strings.ToUpper(strings.TrimSpace(check.Crypto))
		if crypto == "" {
			crypto = strings.ToUpper(strings.TrimSpace(fallback))
		}
		if crypto != "" {
			out[crypto] = struct{}{}
		}
	}
	return out
}

func sortedCryptoKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func envValue(keys ...string) (string, bool) {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value, true
		}
	}
	return "", false
}

func secretEnvValue(keys ...string) (string, bool) {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value, true
		}
		if path := strings.TrimSpace(os.Getenv(key + "_FILE")); path != "" {
			data, err := os.ReadFile(path)
			if err == nil {
				if value := strings.TrimSpace(string(data)); value != "" {
					return value, true
				}
			}
		}
	}
	return "", false
}

func checkAmountFromEnv(prefix string, crypto string, fallback string) string {
	if value, ok := envValue(prefix+"_"+envSuffix(crypto), prefix); ok {
		return value
	}
	return fallback
}

func workerValueFromEnv(prefix string, worker string, globalValue string, fallback string) string {
	if value, ok := envValue(prefix+"_"+envSuffix(worker), prefix); ok {
		return value
	}
	if strings.TrimSpace(globalValue) != "" {
		return strings.TrimSpace(globalValue)
	}
	return fallback
}

func workerSecretFromEnv(prefix string, worker string, globalValue string, fallback string) string {
	if value, ok := secretEnvValue(prefix+"_"+envSuffix(worker), prefix); ok {
		return value
	}
	if strings.TrimSpace(globalValue) != "" {
		return strings.TrimSpace(globalValue)
	}
	return fallback
}

func envSuffix(value string) string {
	return strings.NewReplacer("-", "_").Replace(strings.ToUpper(strings.TrimSpace(value)))
}

func boolEnvValue(key string) (bool, bool) {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return false, false
	}
	return value == "1" || value == "true" || value == "yes" || value == "on", true
}

func intEnvValue(key string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

func secondsValue(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func NewRunner(cfg Config, out io.Writer) *Runner {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.MainURL == "" {
		cfg.MainURL = "http://127.0.0.1:5000"
	}
	if cfg.Crypto == "" {
		cfg.Crypto = "BTC"
	}
	if len(cfg.Cryptos) == 0 {
		cfg.Cryptos = []string{cfg.Crypto}
	}
	if len(cfg.CoverageCryptos) == 0 {
		cfg.CoverageCryptos = append([]string(nil), cfg.Cryptos...)
	}
	if len(cfg.WorkerURLs) == 0 && strings.TrimSpace(cfg.WorkerURL) != "" {
		cfg.WorkerURLs = []NamedURL{{Name: "worker", URL: cfg.WorkerURL}}
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.Requests <= 0 {
		cfg.Requests = 1
	}
	if cfg.OrderRequests <= 0 {
		cfg.OrderRequests = 1
	}
	if cfg.OrderConcurrency <= 0 {
		cfg.OrderConcurrency = 1
	}
	cfg.OrderListStatus = strings.ToUpper(strings.TrimSpace(cfg.OrderListStatus))
	cfg.OrderListCrypto = strings.ToUpper(strings.TrimSpace(cfg.OrderListCrypto))
	if cfg.OrderListLimit <= 0 {
		cfg.OrderListLimit = 50
	}
	if cfg.OrderListMinResults <= 0 {
		cfg.OrderListMinResults = 1
	}
	if cfg.OrderListRequests <= 0 {
		cfg.OrderListRequests = 1
	}
	if cfg.OrderListConcurrency <= 0 {
		cfg.OrderListConcurrency = 1
	}
	if out == nil {
		out = io.Discard
	}
	return &Runner{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		out:    out,
	}
}

func (r *Runner) Run(ctx context.Context) (err error) {
	r.reportMu.Lock()
	r.startedAt = time.Now()
	r.finishedAt = time.Time{}
	r.checks = nil
	r.paymentWallets = map[string]string{}
	r.reportMu.Unlock()
	defer func() {
		r.reportMu.Lock()
		r.finishedAt = time.Now()
		r.reportMu.Unlock()
		if err != nil {
			r.fail(failureCheckName(err), err)
		}
	}()
	if err := r.checkMutatingPlaceholders(); err != nil {
		return err
	}
	if err := r.checkJSONStatus(ctx, "main_healthz", r.cfg.MainURL, "/healthz", "", "", "", "ok"); err != nil {
		return err
	}
	if err := r.checkJSONStatus(ctx, "main_readyz", r.cfg.MainURL, "/readyz", "", "", "", "ready"); err != nil {
		return err
	}
	if r.cfg.Requests > 1 || r.cfg.Concurrency > 1 {
		if err := r.checkMainReadyParallel(ctx); err != nil {
			return err
		}
	}
	if r.cfg.OrderExternalID != "" {
		if err := r.checkOrder(ctx); err != nil {
			return err
		}
		if r.cfg.OrderRequests > 1 || r.cfg.OrderConcurrency > 1 {
			if err := r.checkOrderParallel(ctx); err != nil {
				return err
			}
		}
	} else {
		r.skip("order_lookup", "ORDER_EXTERNAL_ID is not set")
	}
	if r.cfg.OrderListCheck {
		if err := r.checkOrderList(ctx); err != nil {
			return err
		}
		if r.cfg.OrderListRequests > 1 || r.cfg.OrderListConcurrency > 1 {
			if err := r.checkOrderListParallel(ctx); err != nil {
				return err
			}
		}
	} else {
		r.skip("order_list", "DEPLOY_CHECK_ORDER_LIST is not set")
	}
	if r.cfg.MainStatusCheck {
		if err := r.checkMainStatuses(ctx); err != nil {
			return err
		}
	} else {
		r.skip("main_status", "DEPLOY_CHECK_MAIN_STATUS is not set")
	}
	if r.cfg.AdminUsername != "" || r.cfg.AdminPassword != "" {
		if err := r.checkAdmin(ctx); err != nil {
			return err
		}
	} else {
		r.skip("admin_auth", "ADMIN_USERNAME and ADMIN_PASSWORD are not set")
	}
	if err := r.checkCoveragePlan(); err != nil {
		return err
	}
	if r.cfg.AdminUpdateUsername != "" || r.cfg.AdminUpdatePassword != "" {
		if r.cfg.Mutating {
			if err := r.checkAdminRoundTrip(ctx); err != nil {
				return err
			}
		} else {
			r.skip("admin_account_roundtrip", "DEPLOY_CHECK_MUTATING=false")
		}
	}
	if len(r.cfg.WorkerURLs) > 0 {
		for _, worker := range r.cfg.WorkerURLs {
			if err := r.checkWorkerReadiness(ctx, worker); err != nil {
				return err
			}
		}
		if r.cfg.WorkerURL == "" && len(r.cfg.WorkerURLs) == 1 {
			r.cfg.WorkerURL = r.cfg.WorkerURLs[0].URL
		}
		if r.cfg.WorkerURL == "" && (r.cfg.WorkerStatusCheck || r.cfg.WorkerTaskID != "") {
			return errors.New("worker auth checks require DEPLOY_CHECK_WORKER_URL when DEPLOY_CHECK_WORKER_URLS has multiple entries")
		}
		if err := r.checkWorkerAuthChecks(ctx); err != nil {
			return err
		}
	} else {
		r.skip("worker_readyz", "WORKER_URL or WORKER_URLS is not set")
	}
	if len(r.cfg.WorkerAddressChecks) > 0 {
		if r.cfg.Mutating {
			if err := r.checkWorkerAddressChecks(ctx); err != nil {
				return err
			}
		} else {
			r.skip("worker_address", "DEPLOY_CHECK_MUTATING=false")
		}
	}
	if len(r.cfg.PaymentChecks) > 0 {
		if r.cfg.Mutating {
			if err := r.checkPayments(ctx); err != nil {
				return err
			}
		} else {
			r.skip("payment_request", "DEPLOY_CHECK_MUTATING=false")
			r.skip("payment_order", "DEPLOY_CHECK_MUTATING=false")
		}
	}
	if len(r.cfg.PayoutChecks) > 0 {
		if r.cfg.Mutating {
			if err := r.checkPayouts(ctx); err != nil {
				return err
			}
		} else {
			r.skip("payout_dispatch", "DEPLOY_CHECK_MUTATING=false")
			r.skip("payout_status", "DEPLOY_CHECK_MUTATING=false")
			r.skip("payout_order", "DEPLOY_CHECK_MUTATING=false")
		}
	}
	if r.cfg.OrderStatusMatrixCheck {
		if err := r.checkOrderStatusMatrix(ctx); err != nil {
			return err
		}
	} else {
		r.skip("order_status_matrix", "DEPLOY_CHECK_ORDER_STATUS_MATRIX is not set")
	}
	return nil
}

func (r *Runner) checkMutatingPlaceholders() error {
	if !r.cfg.Mutating {
		return nil
	}
	fields := make([]string, 0)
	add := func(name string, value string) {
		if hasPlanPlaceholder(value) {
			fields = append(fields, name)
		}
	}
	add("api_key", r.cfg.APIKey)
	add("order_external_id", r.cfg.OrderExternalID)
	add("admin_username", r.cfg.AdminUsername)
	add("admin_password", r.cfg.AdminPassword)
	add("admin_update_username", r.cfg.AdminUpdateUsername)
	add("admin_update_password", r.cfg.AdminUpdatePassword)
	add("worker_username", r.cfg.WorkerUsername)
	add("worker_password", r.cfg.WorkerPassword)
	for i, check := range r.cfg.PaymentChecks {
		prefix := fmt.Sprintf("payment_checks[%d].", i)
		add(prefix+"amount", check.Amount)
		add(prefix+"external_id", check.ExternalID)
		add(prefix+"external_id_prefix", check.ExternalIDPrefix)
		add(prefix+"callback_url", check.CallbackURL)
	}
	for i, check := range r.cfg.PayoutChecks {
		prefix := fmt.Sprintf("payout_checks[%d].", i)
		add(prefix+"destination", check.Destination)
		add(prefix+"destination_from_payment", check.DestinationFromPayment)
		add(prefix+"amount", check.Amount)
		add(prefix+"fee", check.Fee)
		add(prefix+"external_id", check.ExternalID)
		add(prefix+"external_id_prefix", check.ExternalIDPrefix)
		add(prefix+"callback_url", check.CallbackURL)
	}
	for i, check := range r.cfg.WorkerAddressChecks {
		prefix := fmt.Sprintf("worker_address_checks[%d].", i)
		add(prefix+"url", check.URL)
		add(prefix+"username", check.Username)
		add(prefix+"password", check.Password)
		add(prefix+"amount", check.Amount)
	}
	if len(fields) > 0 {
		return fmt.Errorf("plan_placeholders: replace placeholder values before running mutating checks: %s", strings.Join(fields, ","))
	}
	return nil
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

func (r *Runner) checkCoveragePlan() error {
	if err := r.checkPaymentCoveragePlan(); err != nil {
		return err
	}
	if err := r.checkPayoutCoveragePlan(); err != nil {
		return err
	}
	return nil
}

func (r *Runner) checkPaymentCoveragePlan() error {
	if !r.cfg.RequirePaymentCoverage {
		r.skip("payment_coverage", "DEPLOY_CHECK_REQUIRE_PAYMENT_COVERAGE is not set")
		return nil
	}
	return r.checkCryptoCoverage("payment_coverage", paymentCheckCryptos(r.cfg.PaymentChecks, r.cfg.Crypto))
}

func (r *Runner) checkPayoutCoveragePlan() error {
	if !r.cfg.RequirePayoutCoverage {
		r.skip("payout_coverage", "DEPLOY_CHECK_REQUIRE_PAYOUT_COVERAGE is not set")
		return nil
	}
	return r.checkCryptoCoverage("payout_coverage", payoutCheckCryptos(r.cfg.PayoutChecks, r.cfg.Crypto))
}

func (r *Runner) checkCryptoCoverage(name string, covered map[string]struct{}) error {
	required := normalizeCryptos(r.cfg.CoverageCryptos)
	if len(required) == 0 {
		required = normalizeCryptos(r.cfg.Cryptos)
	}
	missing := make([]string, 0)
	for _, crypto := range required {
		if _, ok := covered[crypto]; !ok {
			missing = append(missing, crypto)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: missing checks for cryptos %s", name, strings.Join(missing, ","))
	}
	requiredText := strings.Join(required, ",")
	coveredText := strings.Join(sortedCryptoKeys(covered), ",")
	r.okLine(name, map[string]string{"required": requiredText, "covered": coveredText}, "check=%s required=%s covered=%s ok\n", name, requiredText, coveredText)
	return nil
}

func (r *Runner) Report(err error) Report {
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	startedAt := r.startedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	finishedAt := r.finishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now()
	}
	status := "ok"
	errText := ""
	if err != nil {
		status = "failed"
		errText = err.Error()
	}
	checks := make([]ReportCheck, len(r.checks))
	copy(checks, r.checks)
	cryptos := make([]string, len(r.cfg.Cryptos))
	copy(cryptos, r.cfg.Cryptos)
	coverageCryptos := make([]string, len(r.cfg.CoverageCryptos))
	copy(coverageCryptos, r.cfg.CoverageCryptos)
	return Report{
		Status:          status,
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		DurationMS:      finishedAt.Sub(startedAt).Milliseconds(),
		MainURL:         r.cfg.MainURL,
		Cryptos:         cryptos,
		CoverageCryptos: coverageCryptos,
		Mutating:        r.cfg.Mutating,
		Error:           errText,
		Checks:          checks,
	}
}

func (r *Runner) WriteReport(path string, err error) error {
	return WriteReportFile(path, r.Report(err))
}

func WriteReportFile(path string, report Report) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create deploy-check report dir %s: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal deploy-check report: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write deploy-check report %s: %w", path, err)
	}
	return nil
}

func (r *Runner) checkPayments(ctx context.Context) error {
	if !r.cfg.Mutating {
		return errors.New("payment_checks: set DEPLOY_CHECK_MUTATING=true to create payment requests")
	}
	if r.cfg.APIKey == "" {
		return errors.New("payment_checks: API_KEY is required")
	}
	for _, check := range r.cfg.PaymentChecks {
		if err := r.checkPayment(ctx, check); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) checkPayment(ctx context.Context, check PaymentCheck) error {
	crypto := strings.ToUpper(strings.TrimSpace(check.Crypto))
	if crypto == "" {
		crypto = r.cfg.Crypto
	}
	fiat := strings.ToUpper(strings.TrimSpace(check.Fiat))
	if fiat == "" {
		fiat = "USD"
	}
	amount := strings.TrimSpace(check.Amount)
	if amount == "" {
		return fmt.Errorf("payment_check %s: amount is required", paymentCheckName(check, crypto))
	}
	externalID := strings.TrimSpace(check.ExternalID)
	if externalID == "" {
		prefix := strings.TrimSpace(check.ExternalIDPrefix)
		if prefix == "" {
			prefix = "deploy-check-payment-" + strings.ToLower(strings.ReplaceAll(crypto, "-", "_"))
		}
		externalID = fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	callbackURL := strings.TrimSpace(check.CallbackURL)
	if callbackURL == "" {
		callbackURL = "https://example.invalid/deploy-check-payment"
	}
	payload := map[string]any{
		"external_id":  externalID,
		"fiat":         fiat,
		"amount":       amount,
		"callback_url": callbackURL,
	}
	var response map[string]any
	status, body, err := r.requestJSON(ctx, http.MethodPost, r.cfg.MainURL, "/api/v1/"+url.PathEscape(crypto)+"/payment_request", "", "", r.cfg.APIKey, payload, &response)
	if err != nil {
		return fmt.Errorf("payment_request %s: %w", paymentCheckName(check, crypto), err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("payment_request %s: status=%d body=%s", paymentCheckName(check, crypto), status, body)
	}
	wallet := strings.TrimSpace(anyString(response["wallet"]))
	if wallet == "" {
		return fmt.Errorf("payment_request %s: response does not include generated wallet/address", paymentCheckName(check, crypto))
	}
	if statusValue := strings.TrimSpace(anyString(response["status"])); statusValue != "" && !strings.EqualFold(statusValue, "success") {
		return fmt.Errorf("payment_request %s: expected success response, got %q", paymentCheckName(check, crypto), statusValue)
	}
	r.recordPaymentWallet(paymentCheckName(check, crypto), wallet)
	r.okLine("payment_request", map[string]string{"name": paymentCheckName(check, crypto), "crypto": crypto, "external_id": externalID, "wallet": wallet}, "check=payment_request name=%s crypto=%s external_id=%s wallet=%s ok\n", paymentCheckName(check, crypto), crypto, externalID, wallet)
	if err := r.checkPaymentOrder(ctx, check, crypto, externalID, wallet); err != nil {
		return err
	}
	return nil
}

func (r *Runner) checkPaymentOrder(ctx context.Context, check PaymentCheck, crypto string, externalID string, wallet string) error {
	path := "/api/v1/orders/" + url.PathEscape(externalID)
	var payload map[string]any
	status, body, err := r.requestJSON(ctx, http.MethodGet, r.cfg.MainURL, path, "", "", r.cfg.APIKey, nil, &payload)
	if err != nil {
		return fmt.Errorf("payment_order %s: %w", paymentCheckName(check, crypto), err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("payment_order %s: status=%d body=%s", paymentCheckName(check, crypto), status, body)
	}
	order := extractOrderPayload(payload)
	if fmt.Sprint(order["external_id"]) != externalID {
		return fmt.Errorf("payment_order %s: expected external_id %q, got %q", paymentCheckName(check, crypto), externalID, fmt.Sprint(order["external_id"]))
	}
	invoices, ok := order["invoices"].([]any)
	if !ok || len(invoices) == 0 {
		return fmt.Errorf("payment_order %s: order payload does not include invoice details", paymentCheckName(check, crypto))
	}
	if check.ExpectStatus != "" && !jsonContainsStatus(order, check.ExpectStatus) {
		return fmt.Errorf("payment_order %s: expected status %q in order payload", paymentCheckName(check, crypto), check.ExpectStatus)
	}
	if !orderContainsAddress(order, wallet) {
		return fmt.Errorf("payment_order %s: generated wallet/address %q is missing from order payload", paymentCheckName(check, crypto), wallet)
	}
	r.okLine("payment_order", map[string]string{"name": paymentCheckName(check, crypto), "crypto": crypto, "external_id": externalID}, "check=payment_order name=%s crypto=%s external_id=%s ok\n", paymentCheckName(check, crypto), crypto, externalID)
	return nil
}

func (r *Runner) checkPayouts(ctx context.Context) error {
	if !r.cfg.Mutating {
		return errors.New("payout_checks: set DEPLOY_CHECK_MUTATING=true to dispatch payout checks")
	}
	if r.cfg.AdminUsername == "" || r.cfg.AdminPassword == "" {
		return errors.New("payout_checks: ADMIN_USERNAME and ADMIN_PASSWORD are required")
	}
	for _, check := range r.cfg.PayoutChecks {
		if err := r.checkPayout(ctx, check); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) checkPayout(ctx context.Context, check PayoutCheck) error {
	crypto := strings.ToUpper(strings.TrimSpace(check.Crypto))
	if crypto == "" {
		crypto = r.cfg.Crypto
	}
	destination := strings.TrimSpace(check.Destination)
	if destination == "" && strings.TrimSpace(check.DestinationFromPayment) != "" {
		destination = r.paymentWallet(check.DestinationFromPayment)
		if destination == "" {
			return fmt.Errorf("payout_check %s: destination_from_payment %q has no generated wallet", checkName(check, crypto), check.DestinationFromPayment)
		}
	}
	amount := strings.TrimSpace(check.Amount)
	if destination == "" || amount == "" {
		return fmt.Errorf("payout_check %s: destination and amount are required", checkName(check, crypto))
	}
	externalID := strings.TrimSpace(check.ExternalID)
	if externalID == "" {
		prefix := strings.TrimSpace(check.ExternalIDPrefix)
		if prefix == "" {
			prefix = "deploy-check-" + strings.ToLower(strings.ReplaceAll(crypto, "-", "_"))
		}
		externalID = fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	payload := map[string]any{
		"destination":  destination,
		"amount":       amount,
		"external_id":  externalID,
		"callback_url": strings.TrimSpace(check.CallbackURL),
	}
	if strings.TrimSpace(check.Fee) != "" {
		payload["fee"] = strings.TrimSpace(check.Fee)
	}
	if payload["callback_url"] == "" {
		delete(payload, "callback_url")
	}
	var response map[string]any
	status, body, err := r.requestJSON(ctx, http.MethodPost, r.cfg.MainURL, "/api/v1/"+url.PathEscape(crypto)+"/payout", r.cfg.AdminUsername, r.cfg.AdminPassword, "", payload, &response)
	if err != nil {
		return fmt.Errorf("payout_dispatch %s: %w", checkName(check, crypto), err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("payout_dispatch %s: status=%d body=%s", checkName(check, crypto), status, body)
	}
	if got := strings.TrimSpace(anyString(response["external_id"])); got != "" && got != externalID {
		return fmt.Errorf("payout_dispatch %s: expected external_id %q, got %q", checkName(check, crypto), externalID, got)
	}
	dispatchDetails := map[string]string{"name": checkName(check, crypto), "crypto": crypto, "external_id": externalID}
	if taskID := strings.TrimSpace(anyString(response["task_id"])); taskID != "" {
		dispatchDetails["task_id"] = taskID
	}
	if txids := txIDsFromReportPayload(response); len(txids) > 0 {
		dispatchDetails["txids"] = strings.Join(txids, ",")
	}
	r.okLine("payout_dispatch", dispatchDetails, "check=payout_dispatch name=%s crypto=%s external_id=%s ok\n", checkName(check, crypto), crypto, externalID)
	if r.cfg.APIKey == "" {
		r.skip("payout_status name="+checkName(check, crypto), "API_KEY is not set")
		r.skip("payout_order name="+checkName(check, crypto), "API_KEY is not set")
		return nil
	}
	if err := r.checkPayoutStatus(ctx, check, crypto, externalID); err != nil {
		return err
	}
	if err := r.checkPayoutOrder(ctx, check, crypto, externalID); err != nil {
		return err
	}
	return nil
}

func (r *Runner) checkPayoutStatus(ctx context.Context, check PayoutCheck, crypto string, externalID string) error {
	path := "/api/v1/" + url.PathEscape(crypto) + "/payout/status?external_id=" + url.QueryEscape(externalID)
	var payload map[string]any
	status, body, err := r.requestJSON(ctx, http.MethodGet, r.cfg.MainURL, path, "", "", r.cfg.APIKey, nil, &payload)
	if err != nil {
		return fmt.Errorf("payout_status %s: %w", checkName(check, crypto), err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("payout_status %s: status=%d body=%s", checkName(check, crypto), status, body)
	}
	if got := strings.TrimSpace(anyString(payload["external_id"])); got != externalID {
		return fmt.Errorf("payout_status %s: expected external_id %q, got %q", checkName(check, crypto), externalID, got)
	}
	if check.ExpectStatus != "" && !strings.EqualFold(strings.TrimSpace(anyString(payload["status"])), check.ExpectStatus) {
		return fmt.Errorf("payout_status %s: expected status %q, got %q", checkName(check, crypto), check.ExpectStatus, anyString(payload["status"]))
	}
	statusDetails := map[string]string{"name": checkName(check, crypto), "crypto": crypto, "external_id": externalID}
	if taskID := strings.TrimSpace(anyString(payload["task_id"])); taskID != "" {
		statusDetails["task_id"] = taskID
	}
	if txids := txIDsFromReportPayload(payload); len(txids) > 0 {
		statusDetails["txids"] = strings.Join(txids, ",")
	}
	r.okLine("payout_status", statusDetails, "check=payout_status name=%s crypto=%s external_id=%s ok\n", checkName(check, crypto), crypto, externalID)
	return nil
}

func (r *Runner) checkPayoutOrder(ctx context.Context, check PayoutCheck, crypto string, externalID string) error {
	path := "/api/v1/orders/" + url.PathEscape(externalID)
	var payload map[string]any
	status, body, err := r.requestJSON(ctx, http.MethodGet, r.cfg.MainURL, path, "", "", r.cfg.APIKey, nil, &payload)
	if err != nil {
		return fmt.Errorf("payout_order %s: %w", checkName(check, crypto), err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("payout_order %s: status=%d body=%s", checkName(check, crypto), status, body)
	}
	order := extractOrderPayload(payload)
	if fmt.Sprint(order["external_id"]) != externalID {
		return fmt.Errorf("payout_order %s: expected external_id %q, got %q", checkName(check, crypto), externalID, fmt.Sprint(order["external_id"]))
	}
	payouts, ok := order["payouts"].([]any)
	if !ok || len(payouts) == 0 {
		return fmt.Errorf("payout_order %s: order payload does not include payout details", checkName(check, crypto))
	}
	if check.ExpectStatus != "" && !jsonContainsStatus(order, check.ExpectStatus) {
		return fmt.Errorf("payout_order %s: expected status %q in order payload", checkName(check, crypto), check.ExpectStatus)
	}
	orderDetails := map[string]string{"name": checkName(check, crypto), "crypto": crypto, "external_id": externalID}
	if txids := txIDsFromReportPayload(order); len(txids) > 0 {
		orderDetails["txids"] = strings.Join(txids, ",")
	}
	if taskID := firstTaskIDFromOrder(order); taskID != "" {
		orderDetails["task_id"] = taskID
	}
	r.okLine("payout_order", orderDetails, "check=payout_order name=%s external_id=%s ok\n", checkName(check, crypto), externalID)
	return nil
}

func txIDsFromReportPayload(value any) []string {
	out := make([]string, 0)
	seen := map[string]struct{}{}
	var walk func(any)
	walk = func(item any) {
		switch v := item.(type) {
		case map[string]any:
			for key, child := range v {
				if strings.EqualFold(key, "txid") || strings.EqualFold(key, "transaction_hash") {
					addReportTxID(&out, seen, anyString(child))
					continue
				}
				if strings.EqualFold(key, "txids") || strings.EqualFold(key, "result") || strings.EqualFold(key, "transactions") || strings.EqualFold(key, "payouts") {
					walk(child)
				}
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		case []string:
			for _, child := range v {
				addReportTxID(&out, seen, child)
			}
		case string:
			addReportTxID(&out, seen, v)
		}
	}
	walk(value)
	return out
}

func addReportTxID(out *[]string, seen map[string]struct{}, txid string) {
	txid = strings.TrimSpace(txid)
	if txid == "" {
		return
	}
	if _, ok := seen[txid]; ok {
		return
	}
	seen[txid] = struct{}{}
	*out = append(*out, txid)
}

func firstTaskIDFromOrder(order map[string]any) string {
	payouts, ok := order["payouts"].([]any)
	if !ok {
		return ""
	}
	for _, payout := range payouts {
		row, ok := payout.(map[string]any)
		if !ok {
			continue
		}
		if taskID := strings.TrimSpace(anyString(row["task_id"])); taskID != "" {
			return taskID
		}
	}
	return ""
}

func checkName(check PayoutCheck, crypto string) string {
	if strings.TrimSpace(check.Name) != "" {
		return strings.TrimSpace(check.Name)
	}
	if strings.TrimSpace(check.ExternalID) != "" {
		return strings.TrimSpace(check.ExternalID)
	}
	return strings.ToLower(strings.ReplaceAll(crypto, "-", "_"))
}

func paymentCheckName(check PaymentCheck, crypto string) string {
	if strings.TrimSpace(check.Name) != "" {
		return strings.TrimSpace(check.Name)
	}
	if strings.TrimSpace(check.ExternalID) != "" {
		return strings.TrimSpace(check.ExternalID)
	}
	return strings.ToLower(strings.ReplaceAll(crypto, "-", "_"))
}

func (r *Runner) recordPaymentWallet(name string, wallet string) {
	name = strings.ToLower(strings.TrimSpace(name))
	wallet = strings.TrimSpace(wallet)
	if name == "" || wallet == "" {
		return
	}
	if r.paymentWallets == nil {
		r.paymentWallets = map[string]string{}
	}
	r.paymentWallets[name] = wallet
}

func (r *Runner) paymentWallet(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || r.paymentWallets == nil {
		return ""
	}
	return strings.TrimSpace(r.paymentWallets[name])
}

func (r *Runner) checkWorkerReadiness(ctx context.Context, worker NamedURL) error {
	if strings.TrimSpace(worker.URL) == "" {
		return errors.New("worker readiness: worker URL is empty")
	}
	name := strings.TrimSpace(worker.Name)
	if name == "" {
		name = "worker"
	}
	healthName := "worker_healthz"
	readyName := "worker_readyz"
	if name != "worker" {
		healthName = "worker_healthz name=" + name
		readyName = "worker_readyz name=" + name
	}
	if err := r.checkJSONStatus(ctx, healthName, worker.URL, "/healthz", "", "", "", "ok"); err != nil {
		return err
	}
	if err := r.checkJSONStatus(ctx, readyName, worker.URL, "/readyz", "", "", "", "ready"); err != nil {
		return err
	}
	return nil
}

func (r *Runner) checkWorkerAuthChecks(ctx context.Context) error {
	if r.cfg.WorkerStatusCheck {
		if err := r.checkWorkerStatus(ctx); err != nil {
			return err
		}
	}
	if r.cfg.WorkerTaskID != "" {
		if err := r.checkWorkerTask(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) checkWorkerAddressChecks(ctx context.Context) error {
	if !r.cfg.Mutating {
		return errors.New("worker_address_checks: set DEPLOY_CHECK_MUTATING=true to generate worker addresses")
	}
	workerURLs := r.workerURLMap()
	for _, check := range r.cfg.WorkerAddressChecks {
		if err := r.checkWorkerAddress(ctx, check, workerURLs); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) workerURLMap() map[string]string {
	out := make(map[string]string, len(r.cfg.WorkerURLs)+1)
	if strings.TrimSpace(r.cfg.WorkerURL) != "" {
		out["worker"] = strings.TrimSpace(r.cfg.WorkerURL)
	}
	for _, worker := range r.cfg.WorkerURLs {
		name := strings.TrimSpace(worker.Name)
		if name == "" {
			name = "worker"
		}
		if strings.TrimSpace(worker.URL) != "" {
			out[name] = strings.TrimSpace(worker.URL)
		}
	}
	return out
}

func (r *Runner) checkWorkerAddress(ctx context.Context, check WorkerAddressCheck, workerURLs map[string]string) error {
	crypto := strings.ToUpper(strings.TrimSpace(check.Crypto))
	if crypto == "" {
		crypto = r.cfg.Crypto
	}
	baseURL := strings.TrimSpace(check.URL)
	if baseURL == "" && strings.TrimSpace(check.Worker) != "" {
		baseURL = workerURLs[strings.TrimSpace(check.Worker)]
	}
	if baseURL == "" && len(workerURLs) == 1 {
		for _, value := range workerURLs {
			baseURL = value
		}
	}
	if baseURL == "" {
		return fmt.Errorf("worker_address %s: worker URL is required", workerAddressCheckName(check, crypto))
	}
	username := strings.TrimSpace(check.Username)
	password := strings.TrimSpace(check.Password)
	if username == "" {
		username = r.cfg.WorkerUsername
	}
	if password == "" {
		password = r.cfg.WorkerPassword
	}
	if username == "" || password == "" {
		return fmt.Errorf("worker_address %s: worker username and password are required", workerAddressCheckName(check, crypto))
	}
	var body any
	if amount := strings.TrimSpace(check.Amount); amount != "" {
		body = map[string]string{"amount": amount}
	}
	var payload map[string]any
	path := "/" + url.PathEscape(crypto) + "/generate-address"
	status, responseBody, err := r.requestJSON(ctx, http.MethodPost, baseURL, path, username, password, "", body, &payload)
	if err != nil {
		return fmt.Errorf("worker_address %s: %w", workerAddressCheckName(check, crypto), err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("worker_address %s: status=%d body=%s", workerAddressCheckName(check, crypto), status, responseBody)
	}
	address := firstWorkerAddress(payload)
	if address == "" {
		return fmt.Errorf("worker_address %s: response does not include generated address", workerAddressCheckName(check, crypto))
	}
	details := map[string]string{
		"name":    workerAddressCheckName(check, crypto),
		"crypto":  crypto,
		"address": address,
	}
	if worker := strings.TrimSpace(check.Worker); worker != "" {
		details["worker"] = worker
	}
	r.okLine("worker_address", details, "check=worker_address name=%s crypto=%s address=%s ok\n", workerAddressCheckName(check, crypto), crypto, address)
	return nil
}

func firstWorkerAddress(payload map[string]any) string {
	for _, key := range []string{"address", "base58check_address", "payment_request", "addr"} {
		if value := strings.TrimSpace(anyString(payload[key])); value != "" {
			return value
		}
	}
	return ""
}

func workerAddressCheckName(check WorkerAddressCheck, crypto string) string {
	if strings.TrimSpace(check.Name) != "" {
		return strings.TrimSpace(check.Name)
	}
	if strings.TrimSpace(check.Worker) != "" {
		return strings.TrimSpace(check.Worker)
	}
	return strings.ToLower(strings.ReplaceAll(crypto, "-", "_")) + "-address"
}

func (r *Runner) checkMainStatuses(ctx context.Context) error {
	if r.cfg.APIKey == "" {
		return errors.New("main_status: API_KEY is required when main status checks are enabled")
	}
	for _, crypto := range r.cfg.Cryptos {
		crypto = strings.ToUpper(strings.TrimSpace(crypto))
		if crypto == "" {
			continue
		}
		path := "/api/v1/" + url.PathEscape(crypto) + "/status"
		var payload map[string]any
		status, body, err := r.requestJSON(ctx, http.MethodGet, r.cfg.MainURL, path, "", "", r.cfg.APIKey, nil, &payload)
		if err != nil {
			return fmt.Errorf("main_status %s: %w", crypto, err)
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("main_status %s: status=%d body=%s", crypto, status, body)
		}
		serverStatus := strings.TrimSpace(anyString(firstAny(payload, "server", "server_status")))
		if serverStatus == "" {
			return fmt.Errorf("main_status %s: response does not include server status", crypto)
		}
		if strings.EqualFold(serverStatus, "Offline") {
			return fmt.Errorf("main_status %s: server is Offline", crypto)
		}
		if r.cfg.ExpectServerStatus != "" && !strings.EqualFold(serverStatus, r.cfg.ExpectServerStatus) {
			return fmt.Errorf("main_status %s: expected server status %q, got %q", crypto, r.cfg.ExpectServerStatus, serverStatus)
		}
		if balanceErr := strings.TrimSpace(anyString(payload["balance_error"])); balanceErr != "" {
			return fmt.Errorf("main_status %s: balance_error=%s", crypto, balanceErr)
		}
		r.okLine("main_status", map[string]string{"crypto": crypto, "server": serverStatus}, "check=main_status crypto=%s server=%q ok\n", crypto, serverStatus)
	}
	return nil
}

func (r *Runner) checkJSONStatus(ctx context.Context, name, baseURL, path, username, password, apiKey, want string) error {
	var payload map[string]any
	status, body, err := r.requestJSON(ctx, http.MethodGet, baseURL, path, username, password, apiKey, nil, &payload)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s: status=%d body=%s", name, status, body)
	}
	got := strings.TrimSpace(fmt.Sprint(payload["status"]))
	if want != "" && got != want {
		return fmt.Errorf("%s: expected status %q, got %q", name, want, got)
	}
	r.ok(name)
	return nil
}

func (r *Runner) checkMainReadyParallel(ctx context.Context) error {
	requests := r.cfg.Requests
	concurrency := r.cfg.Concurrency
	jobs := make(chan int)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				var payload map[string]any
				status, body, err := r.requestJSON(ctx, http.MethodGet, r.cfg.MainURL, "/readyz", "", "", "", nil, &payload)
				if err != nil {
					errs <- err
					continue
				}
				if status < 200 || status >= 300 || fmt.Sprint(payload["status"]) != "ready" {
					errs <- fmt.Errorf("status=%d body=%s", status, body)
				}
			}
		}()
	}
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return fmt.Errorf("main_readyz_parallel: %w", err)
		}
	}
	r.ok(fmt.Sprintf("main_readyz_parallel requests=%d concurrency=%d", requests, concurrency))
	return nil
}

func (r *Runner) checkOrder(ctx context.Context) error {
	elapsed, err := r.fetchOrder(ctx)
	if err != nil {
		return err
	}
	if err := r.checkOrderLatency("order_lookup", elapsed); err != nil {
		return err
	}
	r.ok("order_lookup")
	return nil
}

func (r *Runner) fetchOrder(ctx context.Context) (time.Duration, error) {
	if r.cfg.APIKey == "" {
		return 0, errors.New("order_lookup: API_KEY is required when ORDER_EXTERNAL_ID is set")
	}
	path := "/api/v1/orders/" + url.PathEscape(r.cfg.OrderExternalID)
	var payload map[string]any
	start := time.Now()
	status, body, err := r.requestJSON(ctx, http.MethodGet, r.cfg.MainURL, path, "", "", r.cfg.APIKey, nil, &payload)
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, fmt.Errorf("order_lookup: %w", err)
	}
	if status < 200 || status >= 300 {
		return elapsed, fmt.Errorf("order_lookup: status=%d body=%s", status, body)
	}
	order := extractOrderPayload(payload)
	if fmt.Sprint(order["external_id"]) != r.cfg.OrderExternalID {
		return elapsed, fmt.Errorf("order_lookup: expected external_id %q, got %q", r.cfg.OrderExternalID, fmt.Sprint(order["external_id"]))
	}
	if r.cfg.ExpectStatus != "" && !jsonContainsStatus(order, r.cfg.ExpectStatus) {
		return elapsed, fmt.Errorf("order_lookup: expected status %q in order payload", r.cfg.ExpectStatus)
	}
	return elapsed, nil
}

func (r *Runner) checkOrderParallel(ctx context.Context) error {
	requests := r.cfg.OrderRequests
	concurrency := r.cfg.OrderConcurrency
	jobs := make(chan int)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	var maxLatency time.Duration
	var latencyMu sync.Mutex
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				elapsed, err := r.fetchOrder(ctx)
				if err != nil {
					errs <- err
					continue
				}
				latencyMu.Lock()
				if elapsed > maxLatency {
					maxLatency = elapsed
				}
				latencyMu.Unlock()
				if err := r.checkOrderLatency("order_lookup_parallel", elapsed); err != nil {
					errs <- err
				}
			}
		}()
	}
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	r.okLine("order_lookup_parallel", map[string]string{"requests": strconv.Itoa(requests), "concurrency": strconv.Itoa(concurrency), "max_latency_ms": strconv.FormatInt(maxLatency.Milliseconds(), 10)}, "check=order_lookup_parallel requests=%d concurrency=%d max_latency_ms=%d ok\n", requests, concurrency, maxLatency.Milliseconds())
	return nil
}

func (r *Runner) checkOrderLatency(name string, elapsed time.Duration) error {
	if r.cfg.OrderMaxLatency <= 0 {
		return nil
	}
	if elapsed > r.cfg.OrderMaxLatency {
		return fmt.Errorf("%s: latency %dms exceeded max %dms", name, elapsed.Milliseconds(), r.cfg.OrderMaxLatency.Milliseconds())
	}
	return nil
}

func (r *Runner) checkOrderList(ctx context.Context) error {
	elapsed, rows, err := r.fetchOrderList(ctx)
	if err != nil {
		return err
	}
	if err := r.checkOrderListLatency("order_list", elapsed); err != nil {
		return err
	}
	r.okLine("order_list", map[string]string{
		"rows":           strconv.Itoa(rows),
		"limit":          strconv.Itoa(r.cfg.OrderListLimit),
		"status":         r.cfg.OrderListStatus,
		"crypto":         r.cfg.OrderListCrypto,
		"max_latency_ms": strconv.FormatInt(elapsed.Milliseconds(), 10),
		"min_results":    strconv.Itoa(r.cfg.OrderListMinResults),
	}, "check=order_list rows=%d limit=%d status=%q crypto=%q latency_ms=%d ok\n", rows, r.cfg.OrderListLimit, r.cfg.OrderListStatus, r.cfg.OrderListCrypto, elapsed.Milliseconds())
	return nil
}

func (r *Runner) fetchOrderList(ctx context.Context) (time.Duration, int, error) {
	if r.cfg.APIKey == "" {
		return 0, 0, errors.New("order_list: API_KEY is required when order list checks are enabled")
	}
	values := url.Values{}
	values.Set("limit", strconv.Itoa(r.cfg.OrderListLimit))
	if r.cfg.OrderListStatus != "" {
		values.Set("status", r.cfg.OrderListStatus)
	}
	if r.cfg.OrderListCrypto != "" {
		values.Set("crypto", r.cfg.OrderListCrypto)
	}
	path := "/api/v1/orders?" + values.Encode()
	var payload map[string]any
	start := time.Now()
	status, body, err := r.requestJSON(ctx, http.MethodGet, r.cfg.MainURL, path, "", "", r.cfg.APIKey, nil, &payload)
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, 0, fmt.Errorf("order_list: %w", err)
	}
	if status < 200 || status >= 300 {
		return elapsed, 0, fmt.Errorf("order_list: status=%d body=%s", status, body)
	}
	rawOrders, ok := payload["orders"].([]any)
	if !ok {
		return elapsed, 0, fmt.Errorf("order_list: response does not include orders array")
	}
	if len(rawOrders) < r.cfg.OrderListMinResults {
		return elapsed, len(rawOrders), fmt.Errorf("order_list: got %d orders, want at least %d", len(rawOrders), r.cfg.OrderListMinResults)
	}
	if len(rawOrders) > r.cfg.OrderListLimit {
		return elapsed, len(rawOrders), fmt.Errorf("order_list: got %d orders, exceeds limit %d", len(rawOrders), r.cfg.OrderListLimit)
	}
	for i, item := range rawOrders {
		order, ok := item.(map[string]any)
		if !ok {
			return elapsed, len(rawOrders), fmt.Errorf("order_list: order %d is not an object", i)
		}
		if strings.TrimSpace(fmt.Sprint(order["external_id"])) == "" {
			return elapsed, len(rawOrders), fmt.Errorf("order_list: order %d is missing external_id", i)
		}
		if r.cfg.OrderListStatus != "" && !jsonContainsStatus(order, r.cfg.OrderListStatus) {
			return elapsed, len(rawOrders), fmt.Errorf("order_list: order %s does not include status %q", fmt.Sprint(order["external_id"]), r.cfg.OrderListStatus)
		}
	}
	return elapsed, len(rawOrders), nil
}

func (r *Runner) checkOrderListParallel(ctx context.Context) error {
	requests := r.cfg.OrderListRequests
	concurrency := r.cfg.OrderListConcurrency
	jobs := make(chan int)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	var maxLatency time.Duration
	var minRows int
	var latencyMu sync.Mutex
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				elapsed, rows, err := r.fetchOrderList(ctx)
				if err != nil {
					errs <- err
					continue
				}
				latencyMu.Lock()
				if elapsed > maxLatency {
					maxLatency = elapsed
				}
				if minRows == 0 || rows < minRows {
					minRows = rows
				}
				latencyMu.Unlock()
				if err := r.checkOrderListLatency("order_list_parallel", elapsed); err != nil {
					errs <- err
				}
			}
		}()
	}
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	r.okLine("order_list_parallel", map[string]string{
		"requests":       strconv.Itoa(requests),
		"concurrency":    strconv.Itoa(concurrency),
		"max_latency_ms": strconv.FormatInt(maxLatency.Milliseconds(), 10),
		"min_rows":       strconv.Itoa(minRows),
		"limit":          strconv.Itoa(r.cfg.OrderListLimit),
		"status":         r.cfg.OrderListStatus,
		"crypto":         r.cfg.OrderListCrypto,
	}, "check=order_list_parallel requests=%d concurrency=%d min_rows=%d max_latency_ms=%d ok\n", requests, concurrency, minRows, maxLatency.Milliseconds())
	return nil
}

type orderStatusCryptoPair struct {
	Status string
	Crypto string
}

func (r *Runner) checkOrderStatusMatrix(ctx context.Context) error {
	if r.cfg.APIKey == "" {
		return errors.New("order_status_matrix: API_KEY is required when order status matrix checks are enabled")
	}
	pairs, statuses, cryptos, totalOrders, pages, err := r.fetchOrderStatusMatrix(ctx)
	if err != nil {
		return err
	}
	if totalOrders == 0 {
		return errors.New("order_status_matrix: /api/v1/orders returned no orders")
	}
	if r.cfg.OrderStatusMatrixMin > 0 && len(statuses) < r.cfg.OrderStatusMatrixMin {
		return fmt.Errorf("order_status_matrix: found %d statuses, want at least %d", len(statuses), r.cfg.OrderStatusMatrixMin)
	}
	for _, want := range r.cfg.OrderStatusMatrixWant {
		if !containsString(statuses, want) {
			return fmt.Errorf("order_status_matrix: expected status %s was not found in /api/v1/orders", want)
		}
	}
	oldStatus := r.cfg.OrderListStatus
	oldCrypto := r.cfg.OrderListCrypto
	oldMin := r.cfg.OrderListMinResults
	oldLimit := r.cfg.OrderListLimit
	defer func() {
		r.cfg.OrderListStatus = oldStatus
		r.cfg.OrderListCrypto = oldCrypto
		r.cfg.OrderListMinResults = oldMin
		r.cfg.OrderListLimit = oldLimit
	}()
	r.cfg.OrderListMinResults = 1
	if r.cfg.OrderListLimit <= 0 {
		r.cfg.OrderListLimit = 50
	}
	for _, pair := range pairs {
		r.cfg.OrderListStatus = pair.Status
		r.cfg.OrderListCrypto = pair.Crypto
		elapsed, rows, err := r.fetchOrderList(ctx)
		if err != nil {
			return fmt.Errorf("order_status_matrix %s/%s: %w", pair.Status, pair.Crypto, err)
		}
		if err := r.checkOrderListLatency("order_status_matrix", elapsed); err != nil {
			return fmt.Errorf("order_status_matrix %s/%s: %w", pair.Status, pair.Crypto, err)
		}
		if rows < 1 {
			return fmt.Errorf("order_status_matrix %s/%s: filtered list returned no orders", pair.Status, pair.Crypto)
		}
	}
	r.okLine("order_status_matrix", map[string]string{
		"orders":   strconv.Itoa(totalOrders),
		"pages":    strconv.Itoa(pages),
		"statuses": strings.Join(statuses, ","),
		"cryptos":  strings.Join(cryptos, ","),
		"pairs":    strconv.Itoa(len(pairs)),
	}, "check=order_status_matrix orders=%d pages=%d statuses=%q cryptos=%q pairs=%d ok\n", totalOrders, pages, strings.Join(statuses, ","), strings.Join(cryptos, ","), len(pairs))
	return nil
}

func (r *Runner) fetchOrderStatusMatrix(ctx context.Context) ([]orderStatusCryptoPair, []string, []string, int, int, error) {
	pairSet := map[orderStatusCryptoPair]struct{}{}
	statusSet := map[string]struct{}{}
	cryptoSet := map[string]struct{}{}
	totalOrders := 0
	cursor := ""
	pages := 0
	limit := r.cfg.OrderStatusMatrixLimit
	if limit <= 0 {
		limit = 100
	}
	maxPages := r.cfg.OrderStatusMatrixPages
	if maxPages <= 0 {
		maxPages = 20
	}
	for pages < maxPages {
		values := url.Values{}
		values.Set("limit", strconv.Itoa(limit))
		if cursor != "" {
			values.Set("cursor", cursor)
		}
		path := "/api/v1/orders?" + values.Encode()
		var payload map[string]any
		status, body, err := r.requestJSON(ctx, http.MethodGet, r.cfg.MainURL, path, "", "", r.cfg.APIKey, nil, &payload)
		if err != nil {
			return nil, nil, nil, totalOrders, pages, fmt.Errorf("order_status_matrix: %w", err)
		}
		if status < 200 || status >= 300 {
			return nil, nil, nil, totalOrders, pages, fmt.Errorf("order_status_matrix: status=%d body=%s", status, body)
		}
		rawOrders, ok := payload["orders"].([]any)
		if !ok {
			return nil, nil, nil, totalOrders, pages, errors.New("order_status_matrix: response does not include orders array")
		}
		if len(rawOrders) == 0 {
			break
		}
		pages++
		for i, item := range rawOrders {
			order, ok := item.(map[string]any)
			if !ok {
				return nil, nil, nil, totalOrders, pages, fmt.Errorf("order_status_matrix: order %d is not an object", totalOrders+i)
			}
			if strings.TrimSpace(fmt.Sprint(order["external_id"])) == "" {
				return nil, nil, nil, totalOrders, pages, fmt.Errorf("order_status_matrix: order %d is missing external_id", totalOrders+i)
			}
			pairs := collectOrderStatusCryptoPairs(order)
			if len(pairs) == 0 {
				return nil, nil, nil, totalOrders, pages, fmt.Errorf("order_status_matrix: order %s has no status/crypto pair", fmt.Sprint(order["external_id"]))
			}
			for _, pair := range pairs {
				pairSet[pair] = struct{}{}
				statusSet[pair.Status] = struct{}{}
				cryptoSet[pair.Crypto] = struct{}{}
			}
		}
		totalOrders += len(rawOrders)
		cursor = strings.TrimSpace(anyString(payload["next_cursor"]))
		if cursor == "" {
			break
		}
	}
	if cursor != "" {
		return nil, nil, nil, totalOrders, pages, fmt.Errorf("order_status_matrix: stopped after %d pages with a remaining cursor; increase DEPLOY_CHECK_ORDER_STATUS_MATRIX_PAGES", maxPages)
	}
	pairs := make([]orderStatusCryptoPair, 0, len(pairSet))
	for pair := range pairSet {
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Status == pairs[j].Status {
			return pairs[i].Crypto < pairs[j].Crypto
		}
		return pairs[i].Status < pairs[j].Status
	})
	return pairs, sortedCryptoKeys(statusSet), sortedCryptoKeys(cryptoSet), totalOrders, pages, nil
}

func (r *Runner) checkOrderListLatency(name string, elapsed time.Duration) error {
	if r.cfg.OrderListMaxLatency <= 0 {
		return nil
	}
	if elapsed > r.cfg.OrderListMaxLatency {
		return fmt.Errorf("%s: latency %dms exceeded max %dms", name, elapsed.Milliseconds(), r.cfg.OrderListMaxLatency.Milliseconds())
	}
	return nil
}

func (r *Runner) checkAdmin(ctx context.Context) error {
	if r.cfg.AdminUsername == "" || r.cfg.AdminPassword == "" {
		return errors.New("admin_auth: ADMIN_USERNAME and ADMIN_PASSWORD are both required")
	}
	return r.checkAdminCredentials(ctx, "admin_auth", r.cfg.AdminUsername, r.cfg.AdminPassword)
}

func (r *Runner) checkAdminCredentials(ctx context.Context, name string, username string, password string) error {
	path := "/api/v1/" + url.PathEscape(r.cfg.Crypto) + "/server"
	var payload map[string]any
	status, body, err := r.requestJSON(ctx, http.MethodGet, r.cfg.MainURL, path, username, password, "", nil, &payload)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s: status=%d body=%s", name, status, body)
	}
	r.ok(name)
	return nil
}

func (r *Runner) checkAdminRoundTrip(ctx context.Context) error {
	if !r.cfg.Mutating {
		return errors.New("admin_account_roundtrip: set DEPLOY_CHECK_MUTATING=true to run account update checks")
	}
	if r.cfg.AdminUsername == "" || r.cfg.AdminPassword == "" {
		return errors.New("admin_account_roundtrip: ADMIN_USERNAME and ADMIN_PASSWORD are required")
	}
	nextUsername := strings.TrimSpace(r.cfg.AdminUpdateUsername)
	if nextUsername == "" {
		nextUsername = r.cfg.AdminUsername
	}
	nextPassword := strings.TrimSpace(r.cfg.AdminUpdatePassword)
	if nextPassword == "" {
		return errors.New("admin_account_roundtrip: ADMIN_UPDATE_PASSWORD is required")
	}
	if err := r.patchAdminAccount(ctx, "admin_account_update", r.cfg.AdminUsername, r.cfg.AdminPassword, r.cfg.AdminPassword, nextUsername, nextPassword); err != nil {
		return err
	}
	if err := r.checkAdminCredentials(ctx, "admin_account_updated_auth", nextUsername, nextPassword); err != nil {
		return err
	}
	revertErr := r.patchAdminAccount(ctx, "admin_account_revert", nextUsername, nextPassword, nextPassword, r.cfg.AdminUsername, r.cfg.AdminPassword)
	if revertErr != nil {
		return revertErr
	}
	if err := r.checkAdminCredentials(ctx, "admin_account_reverted_auth", r.cfg.AdminUsername, r.cfg.AdminPassword); err != nil {
		return err
	}
	r.ok("admin_account_roundtrip")
	return nil
}

func (r *Runner) patchAdminAccount(ctx context.Context, name, authUser, authPass, currentPassword, username, newPassword string) error {
	req := map[string]string{
		"username":         username,
		"current_password": currentPassword,
		"new_password":     newPassword,
		"confirm_password": newPassword,
	}
	var payload map[string]any
	status, body, err := r.requestJSON(ctx, http.MethodPatch, r.cfg.MainURL, "/api/v1/admin/account", authUser, authPass, "", req, &payload)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if status < 200 || status >= 300 || fmt.Sprint(payload["status"]) != "success" {
		return fmt.Errorf("%s: status=%d body=%s", name, status, body)
	}
	r.ok(name)
	return nil
}

func (r *Runner) checkWorkerStatus(ctx context.Context) error {
	if r.cfg.WorkerUsername == "" || r.cfg.WorkerPassword == "" {
		return errors.New("worker_status: WORKER_USERNAME and WORKER_PASSWORD are required")
	}
	path := "/" + url.PathEscape(r.cfg.Crypto) + "/status"
	var payload any
	status, body, err := r.requestJSON(ctx, http.MethodPost, r.cfg.WorkerURL, path, r.cfg.WorkerUsername, r.cfg.WorkerPassword, "", map[string]any{}, &payload)
	if err != nil {
		return fmt.Errorf("worker_status: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("worker_status: status=%d body=%s", status, body)
	}
	r.ok("worker_status")
	return nil
}

func (r *Runner) checkWorkerTask(ctx context.Context) error {
	if r.cfg.WorkerUsername == "" || r.cfg.WorkerPassword == "" {
		return errors.New("worker_task: WORKER_USERNAME and WORKER_PASSWORD are required")
	}
	path := "/" + url.PathEscape(r.cfg.Crypto) + "/task/" + url.PathEscape(r.cfg.WorkerTaskID)
	var payload map[string]any
	status, body, err := r.requestJSON(ctx, http.MethodGet, r.cfg.WorkerURL, path, r.cfg.WorkerUsername, r.cfg.WorkerPassword, "", nil, &payload)
	if err != nil {
		return fmt.Errorf("worker_task: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("worker_task: status=%d body=%s", status, body)
	}
	if strings.TrimSpace(fmt.Sprint(payload["status"])) == "" {
		return errors.New("worker_task: response does not include status")
	}
	r.ok("worker_task")
	return nil
}

func (r *Runner) requestJSON(ctx context.Context, method, baseURL, path, username, password, apiKey string, body any, out any) (int, string, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, joinURL(baseURL, path), reader)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}
	if apiKey != "" {
		req.Header.Set("X-Shkeeper-Api-Key", apiKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, "", err
	}
	if len(data) > 0 && out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, string(data), err
		}
	}
	return resp.StatusCode, string(data), nil
}

func (r *Runner) ok(name string) {
	r.record("ok", name, nil, "", "")
	_, _ = fmt.Fprintf(r.out, "check=%s ok\n", name)
}

func (r *Runner) skip(name, reason string) {
	r.record("skipped", name, nil, reason, "")
	_, _ = fmt.Fprintf(r.out, "check=%s skipped reason=%q\n", name, reason)
}

func (r *Runner) okLine(name string, details map[string]string, format string, args ...any) {
	r.record("ok", name, details, "", "")
	_, _ = fmt.Fprintf(r.out, format, args...)
}

func (r *Runner) fail(name string, err error) {
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	r.record("failed", name, nil, "", errText)
}

func (r *Runner) record(status string, name string, details map[string]string, reason string, errText string) {
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	if details != nil {
		copied := make(map[string]string, len(details))
		for key, value := range details {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			copied[key] = strings.TrimSpace(value)
		}
		details = copied
	}
	r.checks = append(r.checks, ReportCheck{
		Name:      strings.TrimSpace(name),
		Status:    status,
		Details:   details,
		Reason:    reason,
		Error:     errText,
		Timestamp: time.Now(),
	})
}

func failureCheckName(err error) string {
	if err == nil {
		return "unknown"
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "unknown"
	}
	before, _, ok := strings.Cut(text, ":")
	if ok && strings.TrimSpace(before) != "" {
		return strings.TrimSpace(before)
	}
	parts := strings.Fields(text)
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}

func joinURL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func jsonContainsStatus(value any, want string) bool {
	want = strings.ToUpper(strings.TrimSpace(want))
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if strings.EqualFold(key, "status") && strings.EqualFold(strings.TrimSpace(fmt.Sprint(item)), want) {
				return true
			}
			if jsonContainsStatus(item, want) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if jsonContainsStatus(item, want) {
				return true
			}
		}
	}
	return false
}

func collectOrderStatusCryptoPairs(value any) []orderStatusCryptoPair {
	out := map[orderStatusCryptoPair]struct{}{}
	if order, ok := value.(map[string]any); ok {
		collectOrderSourcePairs(order["invoices"], out)
		collectOrderSourcePairs(order["payouts"], out)
		if len(out) == 0 {
			collectDirectStatusCryptoPair(order, out)
		}
	}
	pairs := make([]orderStatusCryptoPair, 0, len(out))
	for pair := range out {
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Status == pairs[j].Status {
			return pairs[i].Crypto < pairs[j].Crypto
		}
		return pairs[i].Status < pairs[j].Status
	})
	return pairs
}

func collectOrderSourcePairs(value any, out map[orderStatusCryptoPair]struct{}) {
	switch v := value.(type) {
	case map[string]any:
		collectDirectStatusCryptoPair(v, out)
	case []any:
		for _, item := range v {
			collectOrderSourcePairs(item, out)
		}
	}
}

func collectDirectStatusCryptoPair(row map[string]any, out map[orderStatusCryptoPair]struct{}) {
	status := ""
	crypto := ""
	for key, item := range row {
		switch {
		case strings.EqualFold(key, "status"):
			status = strings.ToUpper(strings.TrimSpace(fmt.Sprint(item)))
		case strings.EqualFold(key, "crypto"):
			crypto = strings.ToUpper(strings.TrimSpace(fmt.Sprint(item)))
		}
	}
	if status != "" && crypto != "" {
		out[orderStatusCryptoPair{Status: status, Crypto: crypto}] = struct{}{}
	}
}

func orderContainsAddress(value any, address string) bool {
	address = strings.TrimSpace(address)
	if address == "" {
		return false
	}
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if (strings.EqualFold(key, "addr") || strings.EqualFold(key, "address") || strings.EqualFold(key, "wallet")) && strings.TrimSpace(fmt.Sprint(item)) == address {
				return true
			}
			if orderContainsAddress(item, address) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if orderContainsAddress(item, address) {
				return true
			}
		}
	}
	return false
}

func extractOrderPayload(payload map[string]any) map[string]any {
	if order, ok := payload["order"].(map[string]any); ok {
		return order
	}
	if orders, ok := payload["orders"].([]any); ok && len(orders) > 0 {
		if order, ok := orders[0].(map[string]any); ok {
			return order
		}
	}
	return payload
}

func firstAny(row map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := row[key]; ok {
			return value
		}
	}
	return nil
}

func anyString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func boolEnv(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func intEnv(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}

func splitCSVUpper(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		item := strings.ToUpper(strings.TrimSpace(part))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
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
		if urlValue == "" {
			continue
		}
		out = append(out, NamedURL{Name: name, URL: urlValue})
	}
	return out
}

func secondsEnv(key string, fallback int) int {
	value := intEnv(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}
