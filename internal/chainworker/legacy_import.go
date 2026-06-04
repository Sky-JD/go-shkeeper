package chainworker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type LegacyAccountImportOptions struct {
	Module                string
	DefaultCrypto         string
	LegacyAccountPassword string
	AccountPassword       string
}

type LegacyAccountImportReport struct {
	Rows    int            `json:"rows"`
	Modules map[string]int `json:"modules"`
	Cryptos map[string]int `json:"cryptos"`
}

func ImportLegacyAccountsJSON(ctx context.Context, store *Store, reader io.Reader, opts LegacyAccountImportOptions) (LegacyAccountImportReport, error) {
	dec := json.NewDecoder(reader)
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return LegacyAccountImportReport{}, err
	}
	return importLegacyAccountRows(ctx, store, legacyAccountRows(raw), opts)
}

func ImportLegacyAccountsMariaDB(ctx context.Context, store *Store, legacyDatabaseURL string, opts LegacyAccountImportOptions) (LegacyAccountImportReport, error) {
	if errText := databaseURLConfigError(legacyDatabaseURL); errText != "" {
		return LegacyAccountImportReport{}, errors.New(errText)
	}
	legacyDB, err := sql.Open("mysql", parseDatabaseURL(legacyDatabaseURL))
	if err != nil {
		return LegacyAccountImportReport{}, err
	}
	defer legacyDB.Close()
	legacyDB.SetMaxOpenConns(2)
	legacyDB.SetMaxIdleConns(1)
	legacyDB.SetConnMaxLifetime(5 * time.Minute)
	if err := legacyDB.PingContext(ctx); err != nil {
		return LegacyAccountImportReport{}, err
	}
	rows, err := legacyAccountRowsFromMariaDB(ctx, legacyDB, opts)
	if err != nil {
		return LegacyAccountImportReport{}, err
	}
	return importLegacyAccountRows(ctx, store, rows, opts)
}

func importLegacyAccountRows(ctx context.Context, store *Store, rows []map[string]any, opts LegacyAccountImportOptions) (LegacyAccountImportReport, error) {
	report := LegacyAccountImportReport{Modules: map[string]int{}, Cryptos: map[string]int{}}
	for _, row := range rows {
		account, err := legacyAccountFromRow(row, opts)
		if err != nil {
			return LegacyAccountImportReport{}, err
		}
		if account.Address == "" || account.PrivateKeyHex == "" {
			continue
		}
		if err := store.UpsertAccount(ctx, &account); err != nil {
			return LegacyAccountImportReport{}, err
		}
		report.Rows++
		report.Modules[account.Module]++
		report.Cryptos[account.Crypto]++
	}
	return report, nil
}

func legacyAccountRowsFromMariaDB(ctx context.Context, db *sql.DB, opts LegacyAccountImportOptions) ([]map[string]any, error) {
	if exists, err := legacyTableExists(ctx, db, "wallets"); err != nil {
		return nil, err
	} else if exists {
		return legacyWalletRowsFromMariaDB(ctx, db)
	}
	if exists, err := legacyTableExists(ctx, db, "tron_keys"); err != nil {
		return nil, err
	} else if exists {
		return legacyDynamicRowsFromMariaDB(ctx, db, "tron_keys", strings.ToUpper(opts.Module))
	}
	if exists, err := legacyTableExists(ctx, db, "chain_account"); err != nil {
		return nil, err
	} else if exists {
		return legacyDynamicRowsFromMariaDB(ctx, db, "chain_account", strings.ToUpper(opts.Module))
	}
	return nil, errors.New("legacy account database has no supported account table: wallets, tron_keys, or chain_account")
}

func legacyWalletRowsFromMariaDB(ctx context.Context, db *sql.DB) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, `SELECT
		COALESCE(w.pub_address, ''),
		COALESCE(w.priv_key, ''),
		COALESCE(a.crypto, IF(w.type = 'fee_deposit', 'BNB', 'BNB-USDT')),
		COALESCE(w.type, ''),
		COALESCE(DATE_FORMAT(w.create_time, '%Y-%m-%d %H:%i:%s'), '')
		FROM wallets w
		LEFT JOIN accounts a ON a.address = w.pub_address
		WHERE COALESCE(w.pub_address, '') <> ''
		ORDER BY w.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var address, privateKey, crypto, kind, createdAt string
		if err := rows.Scan(&address, &privateKey, &crypto, &kind, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"module":      "BNB",
			"pub_address": address,
			"priv_key":    privateKey,
			"crypto":      crypto,
			"type":        kind,
			"create_time": createdAt,
		})
	}
	return out, rows.Err()
}

func legacyDynamicRowsFromMariaDB(ctx context.Context, db *sql.DB, table string, module string) ([]map[string]any, error) {
	columns, err := legacyTableColumns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	addressCol := firstExistingColumn(columns, "address", "pub_address", "public", "base58check_address")
	privateCol := firstExistingColumn(columns, "private_key_encrypted", "private_key_hex", "priv_key", "private", "secret")
	cryptoCol := firstExistingColumn(columns, "crypto", "symbol")
	kindCol := firstExistingColumn(columns, "type", "kind")
	createdCol := firstExistingColumn(columns, "created_at", "create_time")
	if addressCol == "" || privateCol == "" {
		return nil, fmt.Errorf("%s must include an address/public column and private-key column", table)
	}
	selects := []string{
		"COALESCE(" + quoteIdent(addressCol) + ", '')",
		"COALESCE(" + quoteIdent(privateCol) + ", '')",
	}
	if cryptoCol != "" {
		selects = append(selects, "COALESCE("+quoteIdent(cryptoCol)+", '')")
	} else {
		selects = append(selects, "''")
	}
	if kindCol != "" {
		selects = append(selects, "COALESCE("+quoteIdent(kindCol)+", '')")
	} else {
		selects = append(selects, "''")
	}
	if createdCol != "" {
		selects = append(selects, "COALESCE(DATE_FORMAT("+quoteIdent(createdCol)+", '%Y-%m-%d %H:%i:%s'), '')")
	} else {
		selects = append(selects, "''")
	}
	query := "SELECT " + strings.Join(selects, ", ") + " FROM " + quoteIdent(table) + " WHERE COALESCE(" + quoteIdent(addressCol) + ", '') <> ''"
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var address, privateKey, crypto, kind, createdAt string
		if err := rows.Scan(&address, &privateKey, &crypto, &kind, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"module":      module,
			"address":     address,
			"private":     privateKey,
			"crypto":      crypto,
			"type":        kind,
			"create_time": createdAt,
		})
	}
	return out, rows.Err()
}

func legacyTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table).Scan(&count)
	return count > 0, err
}

func legacyTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		out[col] = struct{}{}
	}
	return out, rows.Err()
}

func firstExistingColumn(columns map[string]struct{}, names ...string) string {
	for _, name := range names {
		if _, ok := columns[name]; ok {
			return name
		}
	}
	return ""
}

func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func legacyAccountRows(raw map[string]any) []map[string]any {
	if nested, ok := raw["tables"].(map[string]any); ok {
		raw = nested
	}
	out := []map[string]any{}
	for _, key := range []string{"accounts", "wallets", "tron_keys", "chain_account"} {
		rows, _ := raw[key].([]any)
		for _, item := range rows {
			if row, ok := item.(map[string]any); ok {
				out = append(out, row)
			}
		}
	}
	return out
}

func legacyAccountFromRow(row map[string]any, opts LegacyAccountImportOptions) (Account, error) {
	module := strings.ToUpper(firstLegacyString(row, "module"))
	if module == "" {
		module = strings.ToUpper(strings.TrimSpace(opts.Module))
	}
	if module == "" {
		return Account{}, errors.New("module is required")
	}
	kind := strings.ToLower(firstLegacyString(row, "type"))
	crypto := strings.ToUpper(firstLegacyString(row, "crypto", "symbol"))
	if crypto == "" {
		crypto = strings.ToUpper(strings.TrimSpace(opts.DefaultCrypto))
	}
	if crypto == "" {
		crypto = inferLegacyCrypto(module, kind)
	}
	if crypto == "" {
		return Account{}, errors.New("crypto is required")
	}
	address := strings.TrimSpace(firstLegacyString(row, "address", "pub_address", "public", "base58check_address"))
	if address == "" {
		return Account{}, errors.New("address is required")
	}
	secret, err := legacyPrivateKey(row, opts)
	if err != nil {
		return Account{}, fmt.Errorf("address %s: %w", address, err)
	}
	createdAt := parseLegacyTime(firstLegacyString(row, "created_at", "create_time"))
	return Account{Module: module, Crypto: crypto, Address: address, PrivateKeyHex: secret, CreatedAt: createdAt}, nil
}

func legacyPrivateKey(row map[string]any, opts LegacyAccountImportOptions) (string, error) {
	if encrypted := strings.TrimSpace(firstLegacyString(row, "private_key_encrypted", "private_key_hex")); encrypted != "" && strings.HasPrefix(encrypted, "v1:") {
		return encrypted, nil
	}
	plaintext := strings.TrimSpace(firstLegacyString(row, "private_key_hex", "private"))
	if plaintext == "" {
		legacyEncrypted := strings.TrimSpace(firstLegacyString(row, "legacy_fernet_b64", "priv_key", "private_key_encrypted"))
		if legacyEncrypted == "" {
			return "", nil
		}
		decrypted, err := decryptLegacyFernetSecret(opts.LegacyAccountPassword, legacyEncrypted)
		if err != nil {
			return "", err
		}
		plaintext = decrypted
	} else if looksLegacyFernetSecret(plaintext) {
		decrypted, err := decryptLegacyFernetSecret(opts.LegacyAccountPassword, plaintext)
		if err != nil {
			return "", err
		}
		plaintext = decrypted
	}
	if strings.HasPrefix(plaintext, "v1:") {
		return plaintext, nil
	}
	if strings.TrimSpace(opts.AccountPassword) == "" {
		return "", errors.New("ACCOUNT_PASSWORD is required to encrypt imported private keys")
	}
	return encryptSecret(opts.AccountPassword, plaintext)
}

func looksLegacyFernetSecret(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "Z0FBQUFB") || strings.HasPrefix(value, "gAAAAA")
}

func firstLegacyString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key]; ok {
			switch typed := value.(type) {
			case nil:
				continue
			case string:
				if strings.TrimSpace(typed) != "" {
					return typed
				}
			case json.Number:
				return typed.String()
			default:
				text := strings.TrimSpace(fmt.Sprint(typed))
				if text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func inferLegacyCrypto(module string, kind string) string {
	switch strings.ToUpper(module) {
	case "BNB":
		if kind == "fee_deposit" {
			return "BNB"
		}
		return "BNB-USDT"
	case "TRON":
		return "TRX"
	default:
		return strings.ToUpper(module)
	}
}

func parseLegacyTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "0001-") {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02 15:04:05.000000", "2006-01-02 15:04:05", time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
