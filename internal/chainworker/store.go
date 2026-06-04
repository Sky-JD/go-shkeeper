package chainworker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
)

type Store struct {
	db *sql.DB
}

type Account struct {
	ID            int64     `json:"id"`
	Module        string    `json:"module"`
	Crypto        string    `json:"crypto"`
	Address       string    `json:"address"`
	PrivateKeyHex string    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
}

type Task struct {
	ID        string          `json:"id"`
	Module    string          `json:"module"`
	Crypto    string          `json:"crypto"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Request   json.RawMessage `json:"request"`
	Result    json.RawMessage `json:"result"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type LightningInvoice struct {
	RHash          string
	PaymentRequest string
	Value          decimal.Decimal
	Expiry         string
	State          string
	CreationDate   string
	SettleDate     string
	SentToShkeeper bool
}

func OpenStore(ctx context.Context, cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.DatabaseError) != "" {
		return nil, errors.New(cfg.DatabaseError)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		return nil, err
	}
	maxOpen := cfg.DBMaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 16
	}
	maxIdle := cfg.DBMaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 8
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
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS chain_account (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			module VARCHAR(32) NOT NULL,
			crypto VARCHAR(64) NOT NULL,
			address VARCHAR(128) NOT NULL,
			private_key_hex TEXT NOT NULL,
			created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
			UNIQUE KEY uq_chain_account_address (module, address),
			INDEX ix_chain_account_crypto (module, crypto)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS chain_task (
			id VARCHAR(64) NOT NULL PRIMARY KEY,
			module VARCHAR(32) NOT NULL,
			crypto VARCHAR(64) NOT NULL,
			kind VARCHAR(32) NOT NULL,
			status VARCHAR(32) NOT NULL,
			request_json JSON,
			result_json JSON,
			created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			INDEX ix_chain_task_status (module, status, created_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS bitcoin_lightning_invoice (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			r_hash VARCHAR(255) UNIQUE NOT NULL,
			payment_request VARCHAR(512),
			value DECIMAL(38,18),
			expiry TEXT,
			state TEXT,
			creation_date TEXT,
			settle_date TEXT,
			sent_to_shkeeper BOOLEAN DEFAULT FALSE
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return s.ensureChainAccountSecretColumn(ctx)
}

func (s *Store) ensureChainAccountSecretColumn(ctx context.Context) error {
	var dataType string
	err := s.db.QueryRowContext(ctx, `SELECT DATA_TYPE
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'chain_account' AND COLUMN_NAME = 'private_key_hex'
		LIMIT 1`).Scan(&dataType)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	switch strings.ToLower(dataType) {
	case "text", "mediumtext", "longtext":
		return nil
	default:
		_, err = s.db.ExecContext(ctx, "ALTER TABLE chain_account MODIFY private_key_hex TEXT NOT NULL")
		return err
	}
}

func (s *Store) UpsertLightningInvoice(ctx context.Context, inv LightningInvoice) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO bitcoin_lightning_invoice
		(r_hash, payment_request, value, expiry, state, creation_date, settle_date, sent_to_shkeeper)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE payment_request = VALUES(payment_request), value = VALUES(value),
		expiry = VALUES(expiry), state = VALUES(state), creation_date = VALUES(creation_date),
		settle_date = VALUES(settle_date), sent_to_shkeeper = VALUES(sent_to_shkeeper)`,
		inv.RHash, inv.PaymentRequest, inv.Value, inv.Expiry, inv.State, inv.CreationDate, inv.SettleDate, inv.SentToShkeeper)
	return err
}

func (s *Store) LightningInvoice(ctx context.Context, rHash string) (LightningInvoice, error) {
	var inv LightningInvoice
	err := s.db.QueryRowContext(ctx, `SELECT r_hash, COALESCE(payment_request, ''), COALESCE(value, 0),
		COALESCE(expiry, ''), COALESCE(state, ''), COALESCE(creation_date, ''), COALESCE(settle_date, ''),
		COALESCE(sent_to_shkeeper, 0)
		FROM bitcoin_lightning_invoice WHERE r_hash = ? LIMIT 1`, rHash).Scan(
		&inv.RHash, &inv.PaymentRequest, &inv.Value, &inv.Expiry, &inv.State, &inv.CreationDate, &inv.SettleDate, &inv.SentToShkeeper,
	)
	return inv, err
}

func (s *Store) ListLightningPaymentRequests(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT COALESCE(payment_request, '') FROM bitcoin_lightning_invoice ORDER BY id DESC LIMIT 10000")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var paymentRequest string
		if err := rows.Scan(&paymentRequest); err != nil {
			return nil, err
		}
		if paymentRequest != "" {
			out = append(out, paymentRequest)
		}
	}
	return out, rows.Err()
}

func (s *Store) AddAccount(ctx context.Context, account *Account) error {
	res, err := s.db.ExecContext(
		ctx,
		"INSERT INTO chain_account (module, crypto, address, private_key_hex) VALUES (?, ?, ?, ?)",
		account.Module,
		account.Crypto,
		account.Address,
		account.PrivateKeyHex,
	)
	if err != nil {
		return err
	}
	account.ID, err = res.LastInsertId()
	return err
}

func (s *Store) UpsertAccount(ctx context.Context, account *Account) error {
	createdAt := ""
	if !account.CreatedAt.IsZero() {
		createdAt = account.CreatedAt.Format("2006-01-02 15:04:05.000000")
	}
	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO chain_account (module, crypto, address, private_key_hex, created_at)
		 VALUES (?, ?, ?, ?, COALESCE(NULLIF(?, ''), CURRENT_TIMESTAMP(6)))
		 ON DUPLICATE KEY UPDATE crypto = VALUES(crypto), private_key_hex = VALUES(private_key_hex)`,
		account.Module,
		account.Crypto,
		account.Address,
		account.PrivateKeyHex,
		createdAt,
	)
	if err != nil {
		return err
	}
	if account.ID == 0 {
		account.ID, _ = res.LastInsertId()
	}
	return nil
}

func (s *Store) ListAccounts(ctx context.Context, module string, crypto string) ([]Account, error) {
	rows, err := s.db.QueryContext(
		ctx,
		"SELECT id, module, crypto, address, private_key_hex, created_at FROM chain_account WHERE module = ? AND crypto = ? ORDER BY id DESC LIMIT 10000",
		module,
		crypto,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var account Account
		if err := rows.Scan(&account.ID, &account.Module, &account.Crypto, &account.Address, &account.PrivateKeyHex, &account.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, account)
	}
	return out, rows.Err()
}

func (s *Store) AccountsByModule(ctx context.Context, module string) ([]Account, error) {
	rows, err := s.db.QueryContext(
		ctx,
		"SELECT id, module, crypto, address, private_key_hex, created_at FROM chain_account WHERE module = ? ORDER BY id DESC LIMIT 10000",
		module,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var account Account
		if err := rows.Scan(&account.ID, &account.Module, &account.Crypto, &account.Address, &account.PrivateKeyHex, &account.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, account)
	}
	return out, rows.Err()
}

func (s *Store) AddTask(ctx context.Context, task Task) error {
	_, err := s.db.ExecContext(
		ctx,
		"INSERT INTO chain_task (id, module, crypto, kind, status, request_json, result_json) VALUES (?, ?, ?, ?, ?, ?, ?)",
		task.ID,
		task.Module,
		task.Crypto,
		task.Kind,
		task.Status,
		task.Request,
		task.Result,
	)
	return err
}

func (s *Store) UpdateTask(ctx context.Context, id string, status string, result json.RawMessage) error {
	_, err := s.db.ExecContext(
		ctx,
		"UPDATE chain_task SET status = ?, result_json = ?, updated_at = CURRENT_TIMESTAMP(6) WHERE id = ?",
		status,
		result,
		id,
	)
	return err
}

func (s *Store) Task(ctx context.Context, id string) (Task, error) {
	var task Task
	err := s.db.QueryRowContext(
		ctx,
		"SELECT id, module, crypto, kind, status, COALESCE(request_json, JSON_OBJECT()), COALESCE(result_json, JSON_OBJECT()), created_at, updated_at FROM chain_task WHERE id = ?",
		id,
	).Scan(&task.ID, &task.Module, &task.Crypto, &task.Kind, &task.Status, &task.Request, &task.Result, &task.CreatedAt, &task.UpdatedAt)
	return task, err
}

func isDuplicateStoreError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") || strings.Contains(msg, "already exists") || strings.Contains(msg, "unique")
}
