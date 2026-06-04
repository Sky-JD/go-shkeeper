package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type LegacyImportReport struct {
	Mode           string         `json:"mode"`
	Rows           int            `json:"rows"`
	OrderIndexRows int            `json:"order_index_rows"`
	Tables         map[string]int `json:"tables"`
}

type legacyImportTable struct {
	Name        string
	Columns     []string
	TimeColumns map[string]struct{}
}

func ImportLegacyJSON(ctx context.Context, store *Store, reader io.Reader) (LegacyImportReport, error) {
	dec := json.NewDecoder(reader)
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return LegacyImportReport{}, err
	}
	tables, err := legacyTables(raw)
	if err != nil {
		return LegacyImportReport{}, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return LegacyImportReport{}, err
	}
	defer tx.Rollback()

	report := LegacyImportReport{Mode: "upsert", Tables: map[string]int{}}
	for _, spec := range legacyImportTables() {
		rows, ok := tables[spec.Name]
		if !ok || len(rows) == 0 {
			continue
		}
		for _, row := range rows {
			if err := importLegacyRow(ctx, tx, store.table(spec.Name), spec, row); err != nil {
				return LegacyImportReport{}, fmt.Errorf("import %s: %w", spec.Name, err)
			}
			report.Tables[spec.Name]++
			report.Rows++
		}
	}
	if err := tx.Commit(); err != nil {
		return LegacyImportReport{}, err
	}
	if err := store.RebuildOrderIndex(ctx); err != nil {
		return LegacyImportReport{}, fmt.Errorf("rebuild order index: %w", err)
	}
	if err := store.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", store.table("order_index"))).Scan(&report.OrderIndexRows); err != nil {
		return LegacyImportReport{}, fmt.Errorf("count order index: %w", err)
	}
	return report, nil
}

func ImportLegacyMariaDB(ctx context.Context, store *Store, legacyDatabaseURL string) (LegacyImportReport, error) {
	legacyDatabaseURL = strings.TrimSpace(legacyDatabaseURL)
	if legacyDatabaseURL == "" {
		return LegacyImportReport{}, errors.New("LEGACY_MAIN_DATABASE_URL is required")
	}
	if msg := databaseURLConfigError(legacyDatabaseURL); msg != "" {
		return LegacyImportReport{}, fmt.Errorf("legacy main database URL: %s", msg)
	}
	driver, dsn := parseDatabaseURL(legacyDatabaseURL)
	source, err := sql.Open(driver, dsn)
	if err != nil {
		return LegacyImportReport{}, err
	}
	defer source.Close()
	source.SetMaxOpenConns(2)
	source.SetMaxIdleConns(1)
	source.SetConnMaxLifetime(5 * time.Minute)
	if err := source.PingContext(ctx); err != nil {
		return LegacyImportReport{}, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return LegacyImportReport{}, err
	}
	defer tx.Rollback()

	report := LegacyImportReport{Mode: "mariadb-upsert", Tables: map[string]int{}}
	for _, spec := range legacyImportTables() {
		columns, err := legacyMariaDBColumns(ctx, source, spec)
		if err != nil {
			return LegacyImportReport{}, fmt.Errorf("inspect legacy %s: %w", spec.Name, err)
		}
		if len(columns) == 0 {
			continue
		}
		rows, err := source.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s", quotedColumns(columns), quoteIdentifier(spec.Name)))
		if err != nil {
			return LegacyImportReport{}, fmt.Errorf("read legacy %s: %w", spec.Name, err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			scan := make([]any, len(columns))
			for i := range values {
				scan[i] = &values[i]
			}
			if err := rows.Scan(scan...); err != nil {
				rows.Close()
				return LegacyImportReport{}, fmt.Errorf("scan legacy %s: %w", spec.Name, err)
			}
			row := make(map[string]any, len(columns))
			for i, col := range columns {
				row[col] = values[i]
			}
			if err := importLegacyRow(ctx, tx, store.table(spec.Name), spec, row); err != nil {
				rows.Close()
				return LegacyImportReport{}, fmt.Errorf("import %s: %w", spec.Name, err)
			}
			report.Tables[spec.Name]++
			report.Rows++
		}
		if err := rows.Close(); err != nil {
			return LegacyImportReport{}, fmt.Errorf("close legacy %s rows: %w", spec.Name, err)
		}
		if err := rows.Err(); err != nil {
			return LegacyImportReport{}, fmt.Errorf("iterate legacy %s: %w", spec.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return LegacyImportReport{}, err
	}
	if err := store.RebuildOrderIndex(ctx); err != nil {
		return LegacyImportReport{}, fmt.Errorf("rebuild order index: %w", err)
	}
	if err := store.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", store.table("order_index"))).Scan(&report.OrderIndexRows); err != nil {
		return LegacyImportReport{}, fmt.Errorf("count order index: %w", err)
	}
	return report, nil
}

func legacyMariaDBColumns(ctx context.Context, db *sql.DB, spec legacyImportTable) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT COLUMN_NAME
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
`, spec.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]struct{}{}
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		found[column] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	columns := make([]string, 0, len(spec.Columns))
	for _, column := range spec.Columns {
		if _, ok := found[column]; ok {
			columns = append(columns, column)
		}
	}
	return columns, nil
}

func legacyTables(raw map[string]any) (map[string][]map[string]any, error) {
	if nested, ok := raw["tables"].(map[string]any); ok {
		raw = nested
	}
	out := map[string][]map[string]any{}
	for table, value := range raw {
		rows, ok := value.([]any)
		if !ok {
			continue
		}
		for _, item := range rows {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("table %s includes a non-object row", table)
			}
			out[table] = append(out[table], row)
		}
	}
	return out, nil
}

func legacyImportTables() []legacyImportTable {
	timeCols := func(cols ...string) map[string]struct{} {
		out := make(map[string]struct{}, len(cols))
		for _, col := range cols {
			out[col] = struct{}{}
		}
		return out
	}
	return []legacyImportTable{
		{Name: "user", Columns: []string{"id", "username", "passhash", "api_key", "totp_secret", "totp_enabled", "backup_codes", "totp_enabled_at"}, TimeColumns: timeCols("totp_enabled_at")},
		{Name: "wallet", Columns: []string{"id", "crypto", "serverkey", "pdest", "pfee", "payout", "ppolicy", "pcond", "last_payout_attempt", "enabled", "apikey", "llimit", "ulimit", "recalc", "confirmations", "bkey", "prespolicy", "presamount"}, TimeColumns: timeCols("last_payout_attempt")},
		{Name: "exchange_rate", Columns: []string{"id", "source", "crypto", "fiat", "rate", "fee", "fixed_fee", "fee_policy"}},
		{Name: "invoice", Columns: []string{"id", "crypto", "addr", "external_id", "fiat", "callback_url", "balance_fiat", "balance_crypto", "amount_fiat", "amount_crypto", "exchange_rate", "status", "created_at", "updated_at"}, TimeColumns: timeCols("created_at", "updated_at")},
		{Name: "invoice_address", Columns: []string{"id", "invoice_id", "crypto", "addr", "created_at"}, TimeColumns: timeCols("created_at")},
		{Name: "transaction", Columns: []string{"id", "invoice_id", "txid", "crypto", "amount_crypto", "amount_fiat", "need_more_confirmations", "callback_confirmed", "created_at", "updated_at"}, TimeColumns: timeCols("created_at", "updated_at")},
		{Name: "unconfirmed_transaction", Columns: []string{"id", "invoice_id", "addr", "txid", "crypto", "amount_crypto", "callback_confirmed", "created_at"}, TimeColumns: timeCols("created_at")},
		{Name: "payout", Columns: []string{"id", "created_at", "updated_at", "amount", "crypto", "dest_addr", "success", "error", "callback_url", "task_id", "external_id", "status"}, TimeColumns: timeCols("created_at", "updated_at")},
		{Name: "payout_tx", Columns: []string{"id", "payout_id", "created_at", "updated_at", "txid", "status"}, TimeColumns: timeCols("created_at", "updated_at")},
		{Name: "payout_destination", Columns: []string{"id", "crypto", "addr", "comment"}},
		{Name: "notification", Columns: []string{"id", "txid", "crypto", "amount_crypto", "callback_confirmed", "type", "retries", "object_id", "callback_url", "message", "created_at"}, TimeColumns: timeCols("created_at")},
		{Name: "setting", Columns: []string{"name", "value"}},
		{Name: "bitcoin_lightning_invoice", Columns: []string{"id", "r_hash", "payment_request", "value", "expiry", "state", "creation_date", "settle_date", "sent_to_shkeeper"}},
	}
}

func importLegacyRow(ctx context.Context, tx *sql.Tx, table string, spec legacyImportTable, row map[string]any) error {
	cols := make([]string, 0, len(spec.Columns))
	args := make([]any, 0, len(spec.Columns))
	for _, col := range spec.Columns {
		value, ok := row[col]
		if !ok {
			continue
		}
		cols = append(cols, col)
		args = append(args, normalizeLegacyValue(col, value, spec.TimeColumns))
	}
	if len(cols) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(cols))
	updates := make([]string, 0, len(cols))
	for _, col := range cols {
		quotedCol := quoteIdentifier(col)
		quoted = append(quoted, quotedCol)
		updates = append(updates, quotedCol+" = VALUES("+quotedCol+")")
	}
	sort.Strings(updates)
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON DUPLICATE KEY UPDATE %s",
		table, strings.Join(quoted, ", "), placeholders(len(cols)), strings.Join(updates, ", "))
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func normalizeLegacyValue(column string, value any, timeColumns map[string]struct{}) any {
	if value == nil {
		return nil
	}
	if _, ok := timeColumns[column]; ok {
		if ts, ok := value.(time.Time); ok {
			if ts.IsZero() || ts.Year() <= 1 {
				return nil
			}
			return ts
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" || strings.HasPrefix(text, "0001-") {
			return nil
		}
		return text
	}
	switch typed := value.(type) {
	case json.Number:
		return typed.String()
	case []byte:
		return string(typed)
	case map[string]any:
		if text, ok := typed["__bytes_utf8"].(string); ok {
			return text
		}
		if text, ok := typed["__bytes_b64"].(string); ok {
			decoded, err := base64.StdEncoding.DecodeString(text)
			if err == nil {
				return string(decoded)
			}
		}
		return fmt.Sprint(typed)
	default:
		return typed
	}
}

func quotedColumns(columns []string) string {
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, quoteIdentifier(column))
	}
	return strings.Join(quoted, ", ")
}

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
