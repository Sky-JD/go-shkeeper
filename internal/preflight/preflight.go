package preflight

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/app"
)

type Config struct {
	OutputFile            string
	RequiredTables        []string
	RequireOrderIndexes   bool
	RequireEnabledWallets bool
}

type Report struct {
	Status                string            `json:"status"`
	CheckedAt             string            `json:"checked_at"`
	DatabaseURLConfigured bool              `json:"database_url_configured"`
	RequiredTables        []string          `json:"required_tables"`
	MissingTables         []string          `json:"missing_tables,omitempty"`
	RequiredIndexes       []string          `json:"required_indexes,omitempty"`
	MissingIndexes        []string          `json:"missing_indexes,omitempty"`
	OrderIndexRows        int               `json:"order_index_rows"`
	OrderQueryPlans       []OrderQueryPlan  `json:"order_query_plans,omitempty"`
	EnabledWalletCount    int               `json:"enabled_wallet_count"`
	EnabledCryptos        []string          `json:"enabled_cryptos,omitempty"`
	Blockers              []string          `json:"blockers,omitempty"`
	Warnings              []string          `json:"warnings,omitempty"`
	IndexColumns          map[string]string `json:"index_columns,omitempty"`
	ExplainWarnings       map[string]string `json:"explain_warnings,omitempty"`
	ExplainRows           map[string]int    `json:"explain_rows,omitempty"`
	ExplainAccess         map[string]string `json:"explain_access,omitempty"`
	ExplainPossibleKeys   map[string]string `json:"explain_possible_keys,omitempty"`
	ExplainSelectedTables map[string]string `json:"explain_selected_tables,omitempty"`
	ExplainExtra          map[string]string `json:"explain_extra,omitempty"`
}

type OrderQueryPlan struct {
	Name         string `json:"name"`
	Table        string `json:"table,omitempty"`
	Type         string `json:"type,omitempty"`
	PossibleKeys string `json:"possible_keys,omitempty"`
	Key          string `json:"key,omitempty"`
	Rows         int    `json:"rows,omitempty"`
	Extra        string `json:"extra,omitempty"`
}

func LoadConfigFromEnv() Config {
	return Config{
		OutputFile:            strings.TrimSpace(os.Getenv("CUTOVER_PREFLIGHT_OUTPUT_FILE")),
		RequiredTables:        splitCSV(env("CUTOVER_PREFLIGHT_REQUIRED_TABLES", strings.Join(defaultRequiredTables(), ","))),
		RequireOrderIndexes:   boolEnv("CUTOVER_PREFLIGHT_REQUIRE_ORDER_INDEXES", true),
		RequireEnabledWallets: boolEnv("CUTOVER_PREFLIGHT_REQUIRE_ENABLED_WALLETS", true),
	}
}

func Run(ctx context.Context, cfg Config, logger *slog.Logger) (Report, error) {
	if logger == nil {
		logger = slog.Default()
	}
	requiredTables := uniqueLowerNonEmpty(cfg.RequiredTables)
	if len(requiredTables) == 0 {
		requiredTables = defaultRequiredTables()
	}
	appCfg := app.LoadConfig()
	report := Report{
		Status:                "pass",
		CheckedAt:             time.Now().UTC().Format(time.RFC3339),
		DatabaseURLConfigured: strings.TrimSpace(appCfg.DatabaseURL) != "",
		RequiredTables:        append([]string(nil), requiredTables...),
	}
	if strings.TrimSpace(appCfg.DatabaseConfigError) != "" {
		report.Blockers = append(report.Blockers, appCfg.DatabaseConfigError)
		report.Status = "blocked"
		return report, nil
	}

	store, err := app.OpenStore(ctx, appCfg, logger)
	if err != nil {
		report.Blockers = append(report.Blockers, "open MariaDB: "+err.Error())
		report.Status = "blocked"
		return report, nil
	}
	defer store.Close()

	missingTables, err := missingRequiredTables(ctx, store.DB(), requiredTables)
	if err != nil {
		report.Blockers = append(report.Blockers, "check MariaDB tables: "+err.Error())
		report.Status = "blocked"
		return report, nil
	}
	report.MissingTables = missingTables
	if len(missingTables) > 0 {
		report.Blockers = append(report.Blockers, "missing required MariaDB tables: "+strings.Join(missingTables, ","))
	}
	if !hasAny(missingTables, "invoice", "payout", "order_index") {
		report.RequiredIndexes = requiredOrderIndexes()
		indexColumns, missingIndexes, err := orderIndexMetadata(ctx, store.DB(), report.RequiredIndexes)
		if err != nil {
			report.Blockers = append(report.Blockers, "check MariaDB order indexes: "+err.Error())
		} else {
			report.IndexColumns = indexColumns
			report.MissingIndexes = missingIndexes
			if cfg.RequireOrderIndexes && len(missingIndexes) > 0 {
				report.Blockers = append(report.Blockers, "missing required MariaDB order indexes: "+strings.Join(missingIndexes, ","))
			}
		}
		if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM `order_index`").Scan(&report.OrderIndexRows); err != nil {
			report.Warnings = append(report.Warnings, "count order_index rows: "+err.Error())
		}
		plans, err := orderQueryPlans(ctx, store.DB())
		if err != nil {
			report.Warnings = append(report.Warnings, "explain order hot queries: "+err.Error())
		} else {
			report.OrderQueryPlans = plans
			fillExplainSummary(&report, plans)
		}
	}

	if !containsString(missingTables, "wallet") {
		cryptos, err := enabledWalletCryptos(ctx, store.DB())
		if err != nil {
			report.Blockers = append(report.Blockers, "query enabled wallets: "+err.Error())
		} else {
			report.EnabledCryptos = cryptos
			report.EnabledWalletCount = len(cryptos)
			if cfg.RequireEnabledWallets && len(cryptos) == 0 {
				report.Blockers = append(report.Blockers, "wallet table has no enabled cryptos")
			}
		}
	}

	if len(report.Blockers) > 0 {
		report.Status = "blocked"
	}
	return report, nil
}

func requiredOrderIndexes() []string {
	return []string{
		"order_index.ix_order_index_sort",
		"invoice.ix_invoice_order_external_updated",
		"invoice.ix_invoice_order_status_updated",
		"invoice.ix_invoice_order_crypto_updated",
		"invoice.ix_invoice_list_status_id",
		"invoice.ix_invoice_list_crypto_id",
		"invoice.ix_invoice_list_status_crypto_id",
		"payout.ix_payout_order_external_updated",
		"payout.ix_payout_order_status_updated",
		"payout.ix_payout_order_crypto_updated",
	}
}

func orderIndexMetadata(ctx context.Context, db *sql.DB, required []string) (map[string]string, []string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT TABLE_NAME, INDEX_NAME, GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE()
GROUP BY TABLE_NAME, INDEX_NAME`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	columns := map[string]string{}
	for rows.Next() {
		var table, name, cols string
		if err := rows.Scan(&table, &name, &cols); err != nil {
			return nil, nil, err
		}
		columns[strings.ToLower(table)+"."+name] = cols
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	missing := make([]string, 0)
	for _, key := range required {
		if _, ok := columns[strings.ToLower(key)]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return columns, missing, nil
}

func orderQueryPlans(ctx context.Context, db *sql.DB) ([]OrderQueryPlan, error) {
	checks := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "order_index_sort",
			query: "SELECT external_id, sort_at FROM `order_index` FORCE INDEX (ix_order_index_sort) WHERE sort_at < ? ORDER BY sort_at DESC, external_id DESC LIMIT 20",
			args:  []any{time.Now()},
		},
		{
			name:  "invoice_status",
			query: "SELECT external_id, updated_at FROM `invoice` FORCE INDEX (ix_invoice_order_status_updated) WHERE external_id IS NOT NULL AND external_id <> '' AND status = ? ORDER BY updated_at DESC, external_id DESC LIMIT 20",
			args:  []any{"UNPAID"},
		},
		{
			name:  "invoice_crypto",
			query: "SELECT external_id, updated_at FROM `invoice` FORCE INDEX (ix_invoice_order_crypto_updated) WHERE external_id IS NOT NULL AND external_id <> '' AND crypto = ? ORDER BY updated_at DESC, external_id DESC LIMIT 20",
			args:  []any{"BNB-USDT"},
		},
		{
			name:  "payout_status",
			query: "SELECT external_id, updated_at FROM `payout` FORCE INDEX (ix_payout_order_status_updated) WHERE external_id IS NOT NULL AND external_id <> '' AND status = ? ORDER BY updated_at DESC, external_id DESC LIMIT 20",
			args:  []any{"IN_PROGRESS"},
		},
	}
	out := make([]OrderQueryPlan, 0, len(checks))
	for _, check := range checks {
		plans, err := explainQuery(ctx, db, check.name, check.query, check.args...)
		if err != nil {
			return nil, err
		}
		out = append(out, plans...)
	}
	return out, nil
}

func explainQuery(ctx context.Context, db *sql.DB, name string, query string, args ...any) ([]OrderQueryPlan, error) {
	rows, err := db.QueryContext(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([]OrderQueryPlan, 0)
	for rows.Next() {
		values := make([]sql.NullString, len(cols))
		scan := make([]any, len(cols))
		for i := range values {
			scan[i] = &values[i]
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, err
		}
		row := map[string]string{}
		for i, col := range cols {
			if values[i].Valid {
				row[strings.ToLower(col)] = values[i].String
			}
		}
		out = append(out, OrderQueryPlan{
			Name:         name,
			Table:        row["table"],
			Type:         row["type"],
			PossibleKeys: row["possible_keys"],
			Key:          row["key"],
			Rows:         atoi(row["rows"]),
			Extra:        row["extra"],
		})
	}
	return out, rows.Err()
}

func fillExplainSummary(report *Report, plans []OrderQueryPlan) {
	report.ExplainWarnings = map[string]string{}
	report.ExplainRows = map[string]int{}
	report.ExplainAccess = map[string]string{}
	report.ExplainPossibleKeys = map[string]string{}
	report.ExplainSelectedTables = map[string]string{}
	report.ExplainExtra = map[string]string{}
	for _, plan := range plans {
		if plan.Key == "" {
			report.ExplainWarnings[plan.Name] = "no index selected"
		}
		report.ExplainRows[plan.Name] = plan.Rows
		report.ExplainAccess[plan.Name] = plan.Type
		report.ExplainPossibleKeys[plan.Name] = plan.PossibleKeys
		report.ExplainSelectedTables[plan.Name] = plan.Table
		report.ExplainExtra[plan.Name] = plan.Extra
	}
	if len(report.ExplainWarnings) == 0 {
		report.ExplainWarnings = nil
	}
}

func WriteReport(path string, report Report) error {
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

func missingRequiredTables(ctx context.Context, db *sql.DB, required []string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	present := map[string]struct{}{}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		present[strings.ToLower(strings.TrimSpace(table))] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	missing := make([]string, 0)
	for _, table := range required {
		if _, ok := present[table]; !ok {
			missing = append(missing, table)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

func enabledWalletCryptos(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT crypto FROM wallet WHERE enabled = 1 ORDER BY crypto`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cryptos := make([]string, 0)
	for rows.Next() {
		var crypto string
		if err := rows.Scan(&crypto); err != nil {
			return nil, err
		}
		crypto = strings.ToUpper(strings.TrimSpace(crypto))
		if crypto != "" {
			cryptos = append(cryptos, crypto)
		}
	}
	return cryptos, rows.Err()
}

func defaultRequiredTables() []string {
	return []string{
		"user",
		"wallet",
		"exchange_rate",
		"invoice",
		"invoice_address",
		"transaction",
		"unconfirmed_transaction",
		"payout",
		"payout_tx",
		"payout_destination",
		"notification",
		"setting",
		"order_index",
	}
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

func uniqueLowerNonEmpty(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasAny(values []string, wants ...string) bool {
	for _, want := range wants {
		if containsString(values, want) {
			return true
		}
	}
	return false
}

func env(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
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

func atoi(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	return n
}

func ErrorForStatus(report Report) error {
	if report.Status == "pass" {
		return nil
	}
	return fmt.Errorf("cutover preflight blocked: %s", strings.Join(report.Blockers, "; "))
}
