package chainworker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
)

type Store struct {
	db *sql.DB
}

func NewStoreFromDB(db *sql.DB) *Store {
	return &Store{db: db}
}

type Account struct {
	ID            int64     `json:"id"`
	Module        string    `json:"module"`
	Crypto        string    `json:"crypto"`
	Address       string    `json:"address"`
	PrivateKeyHex string    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
}

type DepositInvoiceAddress struct {
	InvoiceID int64
	Crypto    string
	Address   string
	CreatedAt time.Time
}

type DepositEvent struct {
	ID            int64
	Module        string
	Crypto        string
	Contract      string
	Address       string
	TxID          string
	LogIndex      int64
	BlockNumber   int64
	Confirmations int64
	Status        string
	Attempts      int
	ClaimToken    string
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
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
		`CREATE TABLE IF NOT EXISTS chain_deposit_scan_cursor (
			module VARCHAR(32) NOT NULL,
			crypto VARCHAR(64) NOT NULL,
			contract VARCHAR(128) NOT NULL,
			last_scanned_block BIGINT NOT NULL DEFAULT 0,
			created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			PRIMARY KEY (module, crypto, contract)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS chain_deposit_event (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			module VARCHAR(32) NOT NULL,
			crypto VARCHAR(64) NOT NULL,
			contract VARCHAR(128) NOT NULL,
			address VARCHAR(128) NOT NULL,
			txid VARCHAR(255) NOT NULL,
			log_index BIGINT NOT NULL,
			block_number BIGINT NOT NULL,
			confirmations BIGINT NOT NULL DEFAULT 0,
			status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
			attempts INT NOT NULL DEFAULT 0,
			claim_token VARCHAR(128),
			next_attempt_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
			last_error TEXT,
			created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
			updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			UNIQUE KEY uq_chain_deposit_event_log (module, crypto, txid, log_index),
			INDEX ix_chain_deposit_event_status (module, status, next_attempt_at, id),
			INDEX ix_chain_deposit_event_claim (module, claim_token),
			INDEX ix_chain_deposit_event_address (module, crypto, address),
			INDEX ix_chain_deposit_event_block (module, crypto, block_number)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.ensureChainAccountSecretColumn(ctx); err != nil {
		return err
	}
	return s.ensureDepositEventClaimTokenColumn(ctx)
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

func (s *Store) ensureDepositEventClaimTokenColumn(ctx context.Context) error {
	var columnName string
	err := s.db.QueryRowContext(ctx, `SELECT COLUMN_NAME
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'chain_deposit_event' AND COLUMN_NAME = 'claim_token'
		LIMIT 1`).Scan(&columnName)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE chain_deposit_event ADD COLUMN claim_token VARCHAR(128) NULL AFTER attempts"); err != nil && !isDuplicateStoreError(err) {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "CREATE INDEX ix_chain_deposit_event_claim ON chain_deposit_event (module, claim_token)"); err != nil && !isDuplicateStoreError(err) {
		return err
	}
	return nil
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

func (s *Store) PendingDepositInvoiceAddresses(ctx context.Context, maxAge time.Duration, limit int) ([]DepositInvoiceAddress, error) {
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.id, COALESCE(ia.crypto, ''), COALESCE(ia.addr, ''), i.created_at
		FROM invoice i
		JOIN invoice_address ia ON ia.invoice_id = i.id
		JOIN wallet w ON w.crypto = ia.crypto AND COALESCE(w.enabled, 1) = 1
		WHERE COALESCE(NULLIF(i.status, ''), 'UNPAID') IN ('UNPAID', 'PARTIAL')
		  AND i.created_at >= ?
		  AND COALESCE(ia.addr, '') <> ''
		ORDER BY i.created_at ASC, i.id ASC
		LIMIT ?`, time.Now().Add(-maxAge), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DepositInvoiceAddress
	for rows.Next() {
		var item DepositInvoiceAddress
		if err := rows.Scan(&item.InvoiceID, &item.Crypto, &item.Address, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ActiveDepositInvoiceAddresses(ctx context.Context, maxAge time.Duration, limit int) ([]DepositInvoiceAddress, error) {
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	query := `SELECT i.id, COALESCE(ia.crypto, ''), COALESCE(ia.addr, ''), i.created_at
		FROM invoice i
		JOIN invoice_address ia ON ia.invoice_id = i.id
		JOIN wallet w ON w.crypto = ia.crypto AND COALESCE(w.enabled, 1) = 1
		WHERE COALESCE(NULLIF(i.status, ''), 'UNPAID') IN ('UNPAID', 'PARTIAL')
		  AND i.created_at >= ?
		  AND COALESCE(ia.addr, '') <> ''
		ORDER BY i.created_at ASC, i.id ASC`
	args := []any{time.Now().Add(-maxAge)}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DepositInvoiceAddress
	for rows.Next() {
		var item DepositInvoiceAddress
		if err := rows.Scan(&item.InvoiceID, &item.Crypto, &item.Address, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) DepositScanCursor(ctx context.Context, module string, crypto string, contract string) (int64, bool, error) {
	var block int64
	err := s.db.QueryRowContext(ctx, `SELECT last_scanned_block
		FROM chain_deposit_scan_cursor
		WHERE module = ? AND crypto = ? AND contract = ?
		LIMIT 1`, module, crypto, strings.ToLower(contract)).Scan(&block)
	if err == nil {
		return block, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return 0, false, err
}

func (s *Store) UpsertDepositScanCursor(ctx context.Context, module string, crypto string, contract string, block int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chain_deposit_scan_cursor
		(module, crypto, contract, last_scanned_block)
		VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			last_scanned_block = GREATEST(last_scanned_block, VALUES(last_scanned_block))`,
		module, crypto, strings.ToLower(contract), block)
	return err
}

func (s *Store) InsertDepositEvent(ctx context.Context, event DepositEvent) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT IGNORE INTO chain_deposit_event
		(module, crypto, contract, address, txid, log_index, block_number, confirmations, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'PENDING')`,
		event.Module,
		event.Crypto,
		strings.ToLower(event.Contract),
		strings.ToLower(event.Address),
		strings.ToLower(event.TxID),
		event.LogIndex,
		event.BlockNumber,
		event.Confirmations,
	)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	return rows > 0, err
}

func (s *Store) ClaimPendingDepositEvents(ctx context.Context, module string, limit int, maxAttempts int, claimToken string, lease time.Duration) ([]DepositEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if maxAttempts <= 0 {
		maxAttempts = 20
	}
	if strings.TrimSpace(claimToken) == "" {
		return nil, errors.New("claim token is required")
	}
	if lease <= 0 {
		lease = time.Minute
	}
	staleBefore := time.Now().Add(-lease)
	_, err := s.db.ExecContext(ctx, `UPDATE chain_deposit_event
		SET status = 'IN_PROGRESS', claim_token = ?, updated_at = CURRENT_TIMESTAMP(6)
		WHERE module = ?
		  AND attempts < ?
		  AND COALESCE(next_attempt_at, CURRENT_TIMESTAMP(6)) <= CURRENT_TIMESTAMP(6)
		  AND (status IN ('PENDING', 'FAILED') OR (status = 'IN_PROGRESS' AND updated_at < ?))
		ORDER BY block_number ASC, id ASC
		LIMIT ?`, claimToken, module, maxAttempts, staleBefore, limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, module, crypto, contract, address, txid, log_index,
			block_number, confirmations, status, attempts, COALESCE(claim_token, ''), COALESCE(last_error, ''), created_at, updated_at
		FROM chain_deposit_event
		WHERE module = ?
		  AND status = 'IN_PROGRESS'
		  AND claim_token = ?
		ORDER BY block_number ASC, id ASC
		LIMIT ?`, module, claimToken, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DepositEvent
	for rows.Next() {
		var event DepositEvent
		if err := rows.Scan(&event.ID, &event.Module, &event.Crypto, &event.Contract, &event.Address, &event.TxID, &event.LogIndex, &event.BlockNumber, &event.Confirmations, &event.Status, &event.Attempts, &event.ClaimToken, &event.LastError, &event.CreatedAt, &event.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) MarkDepositEventDelivered(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE chain_deposit_event
		SET status = 'DELIVERED', claim_token = NULL, last_error = NULL
		WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) MarkDepositEventFailed(ctx context.Context, id int64, errText string, retryAfter time.Duration, maxAttempts int) error {
	if retryAfter <= 0 {
		retryAfter = 10 * time.Second
	}
	if maxAttempts <= 0 {
		maxAttempts = 20
	}
	nextAttemptAt := time.Now().Add(retryAfter)
	if len(errText) > 2000 {
		errText = errText[:2000]
	}
	res, err := s.db.ExecContext(ctx, `UPDATE chain_deposit_event
		SET attempts = attempts + 1,
			status = CASE WHEN attempts + 1 >= ? THEN 'DEAD' ELSE 'FAILED' END,
			claim_token = NULL,
			next_attempt_at = ?,
			last_error = ?
		WHERE id = ?`, maxAttempts, nextAttemptAt, errText, id)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func depositEventRetryDelay(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	seconds := 5 * (1 << attempts)
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

func describeDepositEvent(event DepositEvent) string {
	return fmt.Sprintf("%s/%s tx=%s log=%d block=%d address=%s", event.Module, event.Crypto, event.TxID, event.LogIndex, event.BlockNumber, event.Address)
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
