package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type Store struct {
	db      *sql.DB
	dialect string
	logger  *slog.Logger
}

func OpenStore(ctx context.Context, cfg Config, logger *slog.Logger) (*Store, error) {
	if strings.TrimSpace(cfg.DatabaseConfigError) != "" {
		return nil, errors.New(cfg.DatabaseConfigError)
	}
	db, err := sql.Open(cfg.DatabaseDriver, cfg.DatabaseDSN)
	if err != nil {
		return nil, err
	}
	maxOpen := cfg.DBMaxOpenConns
	if maxOpen <= 0 {
		maxOpen = intMax(4, cfg.BalanceWorkers*4)
	}
	maxIdle := cfg.DBMaxIdleConns
	if maxIdle <= 0 {
		maxIdle = intMax(4, cfg.BalanceWorkers)
	}
	connMaxIdleTime := cfg.DBConnMaxIdleTime
	if connMaxIdleTime <= 0 {
		connMaxIdleTime = 3 * time.Minute
	}
	connMaxLifetime := cfg.DBConnMaxLifetime
	if connMaxLifetime <= 0 {
		connMaxLifetime = 30 * time.Minute
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxIdleTime(connMaxIdleTime)
	db.SetConnMaxLifetime(connMaxLifetime)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, dialect: cfg.DatabaseDriver, logger: logger}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) Migrate(ctx context.Context) error {
	statements := mysqlSchema()
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration failed: %w\n%s", err, stmt)
		}
	}
	for _, stmt := range s.columnMigrationStatements() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !isDuplicateSchemaObjectError(err) {
			return fmt.Errorf("column migration failed: %w\n%s", err, stmt)
		}
	}
	for _, stmt := range s.indexStatements() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !isDuplicateSchemaObjectError(err) {
			return fmt.Errorf("index migration failed: %w\n%s", err, stmt)
		}
	}
	if err := s.rebuildOrderIndexIfEmpty(ctx); err != nil {
		return fmt.Errorf("order index migration failed: %w", err)
	}
	return s.ensureDefaultAdmin(ctx)
}

func (s *Store) ensureDefaultAdmin(ctx context.Context) error {
	var id int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT id FROM %s WHERE username = ? LIMIT 1", s.table("user")), "admin").Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (username) VALUES (?)", s.table("user")), "admin")
	return err
}

func (s *Store) table(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (s *Store) nowExpr() string {
	return "CURRENT_TIMESTAMP(6)"
}

func (s *Store) indexStatements() []string {
	return []string{
		"CREATE UNIQUE INDEX uq_invoice_idempotency_key ON " + s.table("invoice") + " (idempotency_key)",
		"CREATE INDEX ix_invoice_external_id ON " + s.table("invoice") + " (external_id)",
		"CREATE INDEX ix_invoice_status_created ON " + s.table("invoice") + " (status, created_at)",
		"CREATE INDEX ix_invoice_order_external_updated ON " + s.table("invoice") + " (external_id, updated_at, id)",
		"CREATE INDEX ix_invoice_order_status_updated ON " + s.table("invoice") + " (status, updated_at, external_id)",
		"CREATE INDEX ix_invoice_order_crypto_updated ON " + s.table("invoice") + " (crypto, updated_at, external_id)",
		"CREATE INDEX ix_invoice_list_status_id ON " + s.table("invoice") + " (status, id)",
		"CREATE INDEX ix_invoice_list_crypto_id ON " + s.table("invoice") + " (crypto, id)",
		"CREATE INDEX ix_invoice_list_status_crypto_id ON " + s.table("invoice") + " (status, crypto, id)",
		"CREATE INDEX ix_invoice_address_addr ON " + s.table("invoice_address") + " (addr)",
		"CREATE INDEX ix_transaction_crypto_txid ON " + s.table("transaction") + " (crypto, txid)",
		"CREATE INDEX ix_transaction_invoice_id ON " + s.table("transaction") + " (invoice_id, id)",
		"CREATE INDEX ix_transaction_callback_confirmations ON " + s.table("transaction") + " (callback_confirmed, need_more_confirmations, created_at)",
		"CREATE INDEX ix_unconfirmed_transaction_callback ON " + s.table("unconfirmed_transaction") + " (callback_confirmed, created_at)",
		"CREATE INDEX ix_unconfirmed_transaction_invoice_id ON " + s.table("unconfirmed_transaction") + " (invoice_id, id)",
		"CREATE INDEX ix_notification_callback_retry ON " + s.table("notification") + " (callback_confirmed, retries, created_at)",
		"CREATE UNIQUE INDEX uq_payout_crypto_external_id ON " + s.table("payout") + " (crypto, external_id)",
		"CREATE INDEX ix_payout_order_external_updated ON " + s.table("payout") + " (external_id, updated_at, id)",
		"CREATE INDEX ix_payout_order_status_updated ON " + s.table("payout") + " (status, updated_at, external_id)",
		"CREATE INDEX ix_payout_order_crypto_updated ON " + s.table("payout") + " (crypto, updated_at, external_id)",
		"CREATE INDEX ix_payout_crypto_amount ON " + s.table("payout") + " (crypto, amount)",
		"CREATE INDEX ix_payout_crypto_status_created ON " + s.table("payout") + " (crypto, status, created_at)",
		"CREATE INDEX ix_payout_status_created ON " + s.table("payout") + " (status, created_at)",
		"CREATE INDEX ix_payout_task_status_created ON " + s.table("payout") + " (task_id, status, created_at)",
		"CREATE UNIQUE INDEX uq_payout_tx_payout_txid ON " + s.table("payout_tx") + " (payout_id, txid)",
		"CREATE INDEX ix_order_index_sort ON " + s.table("order_index") + " (sort_at, external_id)",
	}
}

func (s *Store) columnMigrationStatements() []string {
	return []string{
		"ALTER TABLE " + s.table("invoice") + " ADD COLUMN IF NOT EXISTS idempotency_key BINARY(32) DEFAULT NULL",
		"ALTER TABLE " + s.table("payout") + " ADD COLUMN IF NOT EXISTS fee DECIMAL(38,18) DEFAULT NULL",
		"ALTER TABLE " + s.table("payout") + " ADD COLUMN IF NOT EXISTS fee_asset VARCHAR(64) DEFAULT NULL",
		"ALTER TABLE " + s.table("payout_tx") + " ADD COLUMN IF NOT EXISTS kind VARCHAR(32) DEFAULT 'payout'",
		"ALTER TABLE " + s.table("payout_tx") + " ADD COLUMN IF NOT EXISTS source_addr TEXT DEFAULT NULL",
		"ALTER TABLE " + s.table("payout_tx") + " ADD COLUMN IF NOT EXISTS dest_addr TEXT DEFAULT NULL",
		"ALTER TABLE " + s.table("payout_tx") + " ADD COLUMN IF NOT EXISTS amount DECIMAL(38,18) DEFAULT NULL",
		"ALTER TABLE " + s.table("payout_tx") + " ADD COLUMN IF NOT EXISTS crypto VARCHAR(64) DEFAULT NULL",
		"ALTER TABLE " + s.table("payout_tx") + " ADD COLUMN IF NOT EXISTS error TEXT DEFAULT NULL",
	}
}

func isDuplicateSchemaObjectError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key name") ||
		strings.Contains(msg, "duplicate column name") ||
		strings.Contains(msg, "already exists")
}

func isDuplicateSchemaError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "duplicate key name")
}

func mysqlSchema() []string {
	return []string{
		"CREATE TABLE IF NOT EXISTS `user` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, username VARCHAR(80) NOT NULL UNIQUE, passhash VARCHAR(255), api_key TEXT, totp_secret VARCHAR(64), totp_enabled BOOLEAN DEFAULT FALSE, backup_codes TEXT, totp_enabled_at DATETIME(6)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `wallet` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, crypto VARCHAR(64) NOT NULL UNIQUE, serverkey TEXT, pdest TEXT, pfee TEXT, payout BOOLEAN DEFAULT FALSE, ppolicy VARCHAR(32) DEFAULT 'MANUAL', pcond TEXT, last_payout_attempt DATETIME(6), enabled BOOLEAN DEFAULT TRUE, apikey TEXT, llimit DECIMAL(38,18) DEFAULT 95, ulimit DECIMAL(38,18) DEFAULT 105, recalc INT DEFAULT 0, confirmations INT DEFAULT 1, bkey TEXT, prespolicy VARCHAR(32) DEFAULT 'DISABLE', presamount TEXT) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `exchange_rate` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, source VARCHAR(64) DEFAULT 'dynamic', crypto VARCHAR(64), fiat VARCHAR(16), rate DECIMAL(38,18) DEFAULT 0, fee DECIMAL(38,18) DEFAULT 2, fixed_fee DECIMAL(38,18) DEFAULT 0, fee_policy VARCHAR(64) DEFAULT 'PERCENT_FEE', UNIQUE KEY uq_exchange_rate_crypto (crypto, fiat)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `invoice` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, crypto VARCHAR(64), addr VARCHAR(512), external_id VARCHAR(512), fiat VARCHAR(16), callback_url TEXT, balance_fiat DECIMAL(38,18) DEFAULT 0, balance_crypto DECIMAL(38,18) DEFAULT 0, amount_fiat DECIMAL(38,18), amount_crypto DECIMAL(38,18), exchange_rate DECIMAL(38,18), status VARCHAR(32) DEFAULT 'UNPAID', created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6), updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `invoice_address` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, invoice_id BIGINT NOT NULL, crypto VARCHAR(64), addr VARCHAR(512), created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6), UNIQUE KEY uq_invoice_address_invoice_crypto_addr (invoice_id, crypto, addr)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `transaction` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, invoice_id BIGINT NOT NULL, txid VARCHAR(255), crypto VARCHAR(64), amount_crypto DECIMAL(38,18), amount_fiat DECIMAL(38,18), need_more_confirmations BOOLEAN DEFAULT TRUE, callback_confirmed BOOLEAN DEFAULT FALSE, created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6), updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6), UNIQUE KEY uq_transaction_crypto (crypto, txid, invoice_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `unconfirmed_transaction` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, invoice_id BIGINT NOT NULL, addr TEXT, txid VARCHAR(255), crypto VARCHAR(64), amount_crypto DECIMAL(38,18), callback_confirmed BOOLEAN DEFAULT FALSE, created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6), UNIQUE KEY uq_unconfirmed_transaction_crypto (crypto, txid, invoice_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `payout` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6), updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6), amount DECIMAL(38,18), crypto VARCHAR(64), dest_addr TEXT, success TEXT, error TEXT, callback_url TEXT, task_id VARCHAR(255), external_id VARCHAR(512), status VARCHAR(32) DEFAULT 'IN_PROGRESS', fee DECIMAL(38,18) DEFAULT NULL, fee_asset VARCHAR(64) DEFAULT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `payout_tx` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, payout_id BIGINT NOT NULL, created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6), updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6), txid VARCHAR(255), status VARCHAR(32) DEFAULT 'IN_PROGRESS', kind VARCHAR(32) DEFAULT 'payout', source_addr TEXT, dest_addr TEXT, amount DECIMAL(38,18) DEFAULT NULL, crypto VARCHAR(64) DEFAULT NULL, error TEXT DEFAULT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `payout_destination` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, crypto VARCHAR(64), addr VARCHAR(512) NOT NULL, comment TEXT DEFAULT '', UNIQUE KEY uq_payout_destination_crypto (crypto, addr)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `notification` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, txid VARCHAR(255), crypto VARCHAR(64), amount_crypto DECIMAL(38,18), callback_confirmed BOOLEAN DEFAULT FALSE, type VARCHAR(64) NOT NULL, retries INT DEFAULT 0, object_id BIGINT NOT NULL, callback_url TEXT NOT NULL, message TEXT, created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6), UNIQUE KEY uq_notification_type (type, object_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `setting` (name VARCHAR(255) PRIMARY KEY, value TEXT) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `bitcoin_lightning_invoice` (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, r_hash VARCHAR(255) UNIQUE NOT NULL, payment_request VARCHAR(512), value DECIMAL(38,18), expiry TEXT, state TEXT, creation_date TEXT, settle_date TEXT, sent_to_shkeeper BOOLEAN DEFAULT FALSE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `order_index` (external_id VARCHAR(512) NOT NULL PRIMARY KEY, sort_at DATETIME(6) NOT NULL, updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		"CREATE TABLE IF NOT EXISTS `scheduler_lease` (name VARCHAR(128) NOT NULL PRIMARY KEY, owner VARCHAR(255) NOT NULL, lease_until DATETIME(6) NOT NULL, updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6), INDEX ix_scheduler_lease_until (lease_until)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
	}
}

func intMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}
