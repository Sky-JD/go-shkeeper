package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

func (s *Store) FirstUser(ctx context.Context) (User, error) {
	q := fmt.Sprintf(`SELECT id, username, passhash, api_key, totp_secret, COALESCE(totp_enabled, 0), backup_codes, totp_enabled_at
		FROM %s ORDER BY id LIMIT 1`, s.table("user"))
	var u User
	err := s.db.QueryRowContext(ctx, q).Scan(&u.ID, &u.Username, &u.Passhash, &u.APIKey, &u.TOTPSecret, &u.TOTPEnabled, &u.BackupCodes, &u.TOTPEnabledAt)
	return u, err
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	q := fmt.Sprintf(`SELECT id, username, passhash, api_key, totp_secret, COALESCE(totp_enabled, 0), backup_codes, totp_enabled_at
		FROM %s WHERE id = ?`, s.table("user"))
	var u User
	err := s.db.QueryRowContext(ctx, q, id).Scan(&u.ID, &u.Username, &u.Passhash, &u.APIKey, &u.TOTPSecret, &u.TOTPEnabled, &u.BackupCodes, &u.TOTPEnabledAt)
	return u, err
}

func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	q := fmt.Sprintf(`SELECT id, username, passhash, api_key, totp_secret, COALESCE(totp_enabled, 0), backup_codes, totp_enabled_at
		FROM %s WHERE username = ? LIMIT 1`, s.table("user"))
	var u User
	err := s.db.QueryRowContext(ctx, q, username).Scan(&u.ID, &u.Username, &u.Passhash, &u.APIKey, &u.TOTPSecret, &u.TOTPEnabled, &u.BackupCodes, &u.TOTPEnabledAt)
	return u, err
}

func (s *Store) SetInitialPassword(ctx context.Context, hash []byte) error {
	u, err := s.FirstUser(ctx)
	if err != nil {
		return err
	}
	if u.Passhash.Valid && u.Passhash.String != "" {
		return errors.New("admin password already exists")
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET passhash = ? WHERE id = ?", s.table("user")), string(hash), u.ID)
	return err
}

func (s *Store) UpdateAdminAccount(ctx context.Context, id int64, username string, passwordHash []byte) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("username is required")
	}
	var (
		res sql.Result
		err error
	)
	if len(passwordHash) == 0 {
		res, err = s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET username = ? WHERE id = ?", s.table("user")), username, id)
	} else {
		res, err = s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET username = ?, passhash = ? WHERE id = ?", s.table("user")), username, string(passwordHash), id)
	}
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) EnableUserTOTP(ctx context.Context, id int64, secret string, backupCodesJSON string) error {
	res, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET totp_secret = ?, totp_enabled = 1, backup_codes = ?, totp_enabled_at = %s WHERE id = ?", s.table("user"), s.nowExpr()), secret, backupCodesJSON, id)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DisableUserTOTP(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET totp_secret = NULL, totp_enabled = 0, backup_codes = NULL, totp_enabled_at = NULL WHERE id = ?", s.table("user")), id)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateUserBackupCodes(ctx context.Context, id int64, backupCodesJSON string) error {
	res, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET backup_codes = ? WHERE id = ?", s.table("user")), backupCodesJSON, id)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) WalletByCrypto(ctx context.Context, crypto string) (Wallet, error) {
	q := fmt.Sprintf(`SELECT id, crypto, serverkey, pdest, pfee, COALESCE(payout, 0), COALESCE(ppolicy, 'MANUAL'), pcond,
		last_payout_attempt, COALESCE(enabled, 1), apikey, COALESCE(llimit, 95), COALESCE(ulimit, 105),
		COALESCE(recalc, 0), COALESCE(confirmations, 1), bkey, COALESCE(prespolicy, 'DISABLE'), presamount
		FROM %s WHERE crypto = ? LIMIT 1`, s.table("wallet"))
	var w Wallet
	err := s.db.QueryRowContext(ctx, q, crypto).Scan(
		&w.ID, &w.Crypto, &w.ServerKey, &w.PDest, &w.PFee, &w.Payout, &w.PPolicy, &w.PCond,
		&w.LastAttempt, &w.Enabled, &w.APIKey, &w.LLimit, &w.ULimit, &w.Recalc, &w.Confirmations,
		&w.BKey, &w.PresPolicy, &w.PresAmount,
	)
	return w, err
}

func (s *Store) WalletByAPIKey(ctx context.Context, key string) (Wallet, error) {
	q := fmt.Sprintf(`SELECT id, crypto, serverkey, pdest, pfee, COALESCE(payout, 0), COALESCE(ppolicy, 'MANUAL'), pcond,
		last_payout_attempt, COALESCE(enabled, 1), apikey, COALESCE(llimit, 95), COALESCE(ulimit, 105),
		COALESCE(recalc, 0), COALESCE(confirmations, 1), bkey, COALESCE(prespolicy, 'DISABLE'), presamount
		FROM %s WHERE apikey = ? LIMIT 1`, s.table("wallet"))
	var w Wallet
	err := s.db.QueryRowContext(ctx, q, key).Scan(
		&w.ID, &w.Crypto, &w.ServerKey, &w.PDest, &w.PFee, &w.Payout, &w.PPolicy, &w.PCond,
		&w.LastAttempt, &w.Enabled, &w.APIKey, &w.LLimit, &w.ULimit, &w.Recalc, &w.Confirmations,
		&w.BKey, &w.PresPolicy, &w.PresAmount,
	)
	return w, err
}

func (s *Store) ListWallets(ctx context.Context) ([]Wallet, error) {
	q := fmt.Sprintf(`SELECT id, crypto, serverkey, pdest, pfee, COALESCE(payout, 0), COALESCE(ppolicy, 'MANUAL'), pcond,
		last_payout_attempt, COALESCE(enabled, 1), apikey, COALESCE(llimit, 95), COALESCE(ulimit, 105),
		COALESCE(recalc, 0), COALESCE(confirmations, 1), bkey, COALESCE(prespolicy, 'DISABLE'), presamount
		FROM %s ORDER BY crypto`, s.table("wallet"))
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Wallet
	for rows.Next() {
		var w Wallet
		if err := rows.Scan(&w.ID, &w.Crypto, &w.ServerKey, &w.PDest, &w.PFee, &w.Payout, &w.PPolicy, &w.PCond, &w.LastAttempt, &w.Enabled, &w.APIKey, &w.LLimit, &w.ULimit, &w.Recalc, &w.Confirmations, &w.BKey, &w.PresPolicy, &w.PresAmount); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) EnsureWallet(ctx context.Context, crypto string, apiKey string) error {
	var id int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT id FROM %s WHERE crypto = ? LIMIT 1", s.table("wallet")), crypto).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		existingKey := apiKey
		var fromDB sql.NullString
		_ = s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT apikey FROM %s WHERE apikey IS NOT NULL AND apikey <> '' LIMIT 1", s.table("wallet"))).Scan(&fromDB)
		if fromDB.Valid && fromDB.String != "" {
			existingKey = fromDB.String
		}
		_, err = s.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (crypto, apikey, enabled, llimit, ulimit, recalc, confirmations, ppolicy, prespolicy) VALUES (?, ?, ?, 95, 105, 0, 1, 'MANUAL', 'DISABLE')", s.table("wallet")), crypto, existingKey, true)
		return err
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET apikey = COALESCE(NULLIF(apikey, ''), ?) WHERE crypto = ?", s.table("wallet")), apiKey, crypto)
	return err
}

func (s *Store) SetWalletEnabled(ctx context.Context, crypto string, enabled bool) error {
	res, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET enabled = ? WHERE crypto = ?", s.table("wallet")), enabled, crypto)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetAllWalletAPIKeys(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET apikey = ?", s.table("wallet")), key)
	return err
}

func (s *Store) UpdateWalletServerKey(ctx context.Context, crypto string, key string) error {
	res, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET serverkey = ? WHERE crypto = ?", s.table("wallet")), nullOrText(key), crypto)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateWalletServerHost(ctx context.Context, crypto string, host string) error {
	return s.UpsertSetting(ctx, serverHostSettingName(crypto), host)
}

func (s *Store) WalletServerHost(ctx context.Context, crypto string) (string, error) {
	return s.Setting(ctx, serverHostSettingName(crypto))
}

func (s *Store) UpdateWalletAutopayout(ctx context.Context, crypto string, settings WalletAutopayout) error {
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		pdest = ?, pfee = ?, payout = ?, ppolicy = ?, pcond = ?, llimit = ?, ulimit = ?,
		recalc = ?, confirmations = ?, prespolicy = ?, presamount = ?
		WHERE crypto = ?`, s.table("wallet")),
		nullOrString(settings.PDest), nullOrString(settings.PFee), settings.Payout, settings.PPolicy,
		nullOrString(settings.PCond), settings.LLimit, settings.ULimit, settings.Recalc,
		settings.Confirmations, settings.PresPolicy, nullOrString(settings.PresAmount), crypto)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateWalletLastPayoutAttempt(ctx context.Context, crypto string, attemptedAt time.Time) error {
	res, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET last_payout_attempt = ? WHERE crypto = ?", s.table("wallet")), attemptedAt.UTC(), crypto)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) HasInProgressPayout(ctx context.Context, crypto string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT 1 FROM %s WHERE crypto = ? AND status = ? LIMIT 1", s.table("payout")), strings.ToUpper(strings.TrimSpace(crypto)), PayoutInProgress).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) UpsertPayoutDestination(ctx context.Context, crypto, addr, comment string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (crypto, addr, comment) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE comment = VALUES(comment)`, s.table("payout_destination")), crypto, addr, comment)
	return err
}

func (s *Store) DeletePayoutDestination(ctx context.Context, crypto, addr string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE crypto = ? AND addr = ?", s.table("payout_destination")), crypto, addr)
	return err
}

func (s *Store) ListPayoutDestinations(ctx context.Context, crypto string) ([]PayoutDestination, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT id, COALESCE(crypto, ''), addr, COALESCE(comment, '') FROM %s WHERE crypto = ? ORDER BY id", s.table("payout_destination")), crypto)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PayoutDestination, 0)
	for rows.Next() {
		var p PayoutDestination
		if err := rows.Scan(&p.ID, &p.Crypto, &p.Addr, &p.Comment); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Setting(ctx context.Context, name string) (string, error) {
	var value sql.NullString
	err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT value FROM %s WHERE name = ? LIMIT 1", s.table("setting")), name).Scan(&value)
	if err != nil {
		return "", err
	}
	return nullStringValue(value), nil
}

func (s *Store) UpsertSetting(ctx context.Context, name string, value string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (name, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)`, s.table("setting")), name, value)
	return err
}

func (s *Store) EnsureExchangeRate(ctx context.Context, crypto string, fiat string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (source, crypto, fiat, rate, fee, fixed_fee, fee_policy)
		VALUES ('dynamic', ?, ?, 0, 2, 0, 'PERCENT_FEE')`, s.table("exchange_rate")), crypto, fiat)
	if err != nil && isDuplicateSchemaError(err) {
		return nil
	}
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return nil
	}
	return err
}

func (s *Store) ExchangeRate(ctx context.Context, fiat string, crypto string) (ExchangeRate, error) {
	q := fmt.Sprintf(`SELECT id, COALESCE(source, 'dynamic'), crypto, fiat, COALESCE(rate, 0), COALESCE(fee, 2), COALESCE(fixed_fee, 0), COALESCE(fee_policy, 'PERCENT_FEE')
		FROM %s WHERE fiat = ? AND crypto = ? LIMIT 1`, s.table("exchange_rate"))
	var r ExchangeRate
	err := s.db.QueryRowContext(ctx, q, fiat, crypto).Scan(&r.ID, &r.Source, &r.Crypto, &r.Fiat, &r.Rate, &r.Fee, &r.FixedFee, &r.FeePolicy)
	if errors.Is(err, sql.ErrNoRows) {
		if ensureErr := s.EnsureExchangeRate(ctx, crypto, fiat); ensureErr != nil {
			return r, ensureErr
		}
		err = s.db.QueryRowContext(ctx, q, fiat, crypto).Scan(&r.ID, &r.Source, &r.Crypto, &r.Fiat, &r.Rate, &r.Fee, &r.FixedFee, &r.FeePolicy)
	}
	return r, err
}

func (s *Store) UpdateExchangeRate(ctx context.Context, crypto, fiat, source string, rate decimal.Decimal, updateRate bool, fee decimal.Decimal) error {
	var (
		res sql.Result
		err error
	)
	if updateRate {
		res, err = s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET source = ?, rate = ?, fee = ? WHERE crypto = ? AND fiat = ?", s.table("exchange_rate")), source, rate, fee, crypto, fiat)
	} else {
		res, err = s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET source = ?, fee = ? WHERE crypto = ? AND fiat = ?", s.table("exchange_rate")), source, fee, crypto, fiat)
	}
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateExchangeRateSettings(ctx context.Context, rate ExchangeRate, updateRate bool) error {
	table := s.table("exchange_rate")
	if updateRate {
		_, err := s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (source, crypto, fiat, rate, fee, fixed_fee, fee_policy)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE source = VALUES(source), rate = VALUES(rate), fee = VALUES(fee), fixed_fee = VALUES(fixed_fee), fee_policy = VALUES(fee_policy)`, table),
			rate.Source, rate.Crypto, rate.Fiat, rate.Rate, rate.Fee, rate.FixedFee, rate.FeePolicy)
		return err
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (source, crypto, fiat, rate, fee, fixed_fee, fee_policy)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE source = VALUES(source), fee = VALUES(fee), fixed_fee = VALUES(fixed_fee), fee_policy = VALUES(fee_policy)`, table),
		rate.Source, rate.Crypto, rate.Fiat, rate.Rate, rate.Fee, rate.FixedFee, rate.FeePolicy)
	return err
}

func (s *Store) FindInvoiceByExternalCallbackFiat(ctx context.Context, externalID, callbackURL, fiat string) (Invoice, error) {
	q := fmt.Sprintf(`SELECT id, crypto, COALESCE(addr, ''), COALESCE(external_id, ''), COALESCE(fiat, ''), COALESCE(callback_url, ''),
		COALESCE(balance_fiat, 0), COALESCE(balance_crypto, 0), COALESCE(amount_fiat, 0), COALESCE(amount_crypto, 0),
		COALESCE(exchange_rate, 0), COALESCE(status, 'UNPAID'), created_at, updated_at
		FROM %s WHERE external_id = ? AND callback_url = ? AND fiat = ? LIMIT 1`, s.table("invoice"))
	return s.scanInvoice(ctx, q, externalID, callbackURL, fiat)
}

func (s *Store) InvoiceByID(ctx context.Context, id int64) (Invoice, error) {
	q := fmt.Sprintf(`SELECT id, crypto, COALESCE(addr, ''), COALESCE(external_id, ''), COALESCE(fiat, ''), COALESCE(callback_url, ''),
		COALESCE(balance_fiat, 0), COALESCE(balance_crypto, 0), COALESCE(amount_fiat, 0), COALESCE(amount_crypto, 0),
		COALESCE(exchange_rate, 0), COALESCE(status, 'UNPAID'), created_at, updated_at
		FROM %s WHERE id = ? LIMIT 1`, s.table("invoice"))
	return s.scanInvoice(ctx, q, id)
}

func (s *Store) scanInvoice(ctx context.Context, q string, args ...any) (Invoice, error) {
	var i Invoice
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&i.ID, &i.Crypto, &i.Addr, &i.ExternalID, &i.Fiat, &i.CallbackURL, &i.BalanceFiat, &i.BalanceCrypto, &i.AmountFiat, &i.AmountCrypto, &i.ExchangeRate, &i.Status, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

func (s *Store) CreateInvoice(ctx context.Context, i *Invoice) error {
	q := fmt.Sprintf(`INSERT INTO %s (crypto, addr, external_id, fiat, callback_url, balance_fiat, balance_crypto, amount_fiat, amount_crypto, exchange_rate, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.table("invoice"))
	res, err := s.db.ExecContext(ctx, q, i.Crypto, i.Addr, i.ExternalID, i.Fiat, i.CallbackURL, i.BalanceFiat, i.BalanceCrypto, i.AmountFiat, i.AmountCrypto, i.ExchangeRate, i.Status)
	if err != nil {
		return err
	}
	i.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	return s.refreshOrderIndexForExternalID(ctx, i.ExternalID)
}

func (s *Store) UpdateInvoicePayment(ctx context.Context, i Invoice) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET crypto = ?, addr = ?, fiat = ?, amount_fiat = ?, amount_crypto = ?, exchange_rate = ?, updated_at = %s WHERE id = ?`, s.table("invoice"), s.nowExpr()),
		i.Crypto, i.Addr, i.Fiat, i.AmountFiat, i.AmountCrypto, i.ExchangeRate, i.ID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(i.ExternalID) != "" {
		return s.refreshOrderIndexForExternalID(ctx, i.ExternalID)
	}
	return s.refreshOrderIndexForInvoiceID(ctx, i.ID)
}

func (s *Store) UpdateInvoiceBalance(ctx context.Context, invoiceID int64, balanceFiat, balanceCrypto decimal.Decimal, status string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET balance_fiat = ?, balance_crypto = ?, status = ?, updated_at = %s WHERE id = ?`, s.table("invoice"), s.nowExpr()),
		balanceFiat, balanceCrypto, status, invoiceID)
	if err != nil {
		return err
	}
	return s.refreshOrderIndexForInvoiceID(ctx, invoiceID)
}

func (s *Store) FindInvoiceAddress(ctx context.Context, invoiceID int64, crypto string) (InvoiceAddress, error) {
	q := fmt.Sprintf("SELECT id, invoice_id, crypto, addr, created_at FROM %s WHERE invoice_id = ? AND crypto = ? LIMIT 1", s.table("invoice_address"))
	var a InvoiceAddress
	err := s.db.QueryRowContext(ctx, q, invoiceID, crypto).Scan(&a.ID, &a.InvoiceID, &a.Crypto, &a.Addr, &a.CreatedAt)
	return a, err
}

func (s *Store) AddInvoiceAddress(ctx context.Context, invoiceID int64, crypto, addr string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (invoice_id, crypto, addr) VALUES (?, ?, ?)", s.table("invoice_address")), invoiceID, crypto, addr)
	if err != nil && (isDuplicateSchemaError(err) || strings.Contains(strings.ToLower(err.Error()), "unique")) {
		return nil
	}
	return err
}

func (s *Store) InvoiceByCryptoAddress(ctx context.Context, crypto string, addr string) (Invoice, error) {
	q := fmt.Sprintf(`SELECT i.id, i.crypto, COALESCE(i.addr, ''), COALESCE(i.external_id, ''), COALESCE(i.fiat, ''), COALESCE(i.callback_url, ''),
		COALESCE(i.balance_fiat, 0), COALESCE(i.balance_crypto, 0), COALESCE(i.amount_fiat, 0), COALESCE(i.amount_crypto, 0),
		COALESCE(i.exchange_rate, 0), COALESCE(i.status, 'UNPAID'), i.created_at, i.updated_at
		FROM %s i JOIN %s ia ON ia.invoice_id = i.id
		WHERE ia.crypto = ? AND (ia.addr = ? OR lower(ia.addr) = lower(?)) LIMIT 1`, s.table("invoice"), s.table("invoice_address"))
	invoice, err := s.scanInvoice(ctx, q, crypto, addr, addr)
	if err == nil {
		return invoice, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return invoice, err
	}
	q = fmt.Sprintf(`SELECT id, crypto, COALESCE(addr, ''), COALESCE(external_id, ''), COALESCE(fiat, ''), COALESCE(callback_url, ''),
		COALESCE(balance_fiat, 0), COALESCE(balance_crypto, 0), COALESCE(amount_fiat, 0), COALESCE(amount_crypto, 0),
		COALESCE(exchange_rate, 0), COALESCE(status, 'UNPAID'), created_at, updated_at
		FROM %s WHERE crypto = ? AND status <> ? AND (addr = ? OR lower(addr) = lower(?)) LIMIT 1`, s.table("invoice"))
	return s.scanInvoice(ctx, q, crypto, InvoiceOutgoing, addr, addr)
}

func (s *Store) ExistingTransaction(ctx context.Context, crypto, txid string, invoiceID int64) (Transaction, error) {
	q := fmt.Sprintf(`SELECT id, invoice_id, txid, crypto, COALESCE(amount_crypto, 0), COALESCE(amount_fiat, 0),
		COALESCE(need_more_confirmations, 1), COALESCE(callback_confirmed, 0), created_at, updated_at
		FROM %s WHERE crypto = ? AND txid = ? AND invoice_id = ? LIMIT 1`, s.table("transaction"))
	var t Transaction
	err := s.db.QueryRowContext(ctx, q, crypto, txid, invoiceID).Scan(&t.ID, &t.InvoiceID, &t.TxID, &t.Crypto, &t.AmountCrypto, &t.AmountFiat, &t.NeedMoreConfirmations, &t.CallbackConfirmed, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *Store) ExistingTransactionByCryptoTxID(ctx context.Context, crypto, txid string) (Transaction, error) {
	q := fmt.Sprintf(`SELECT id, invoice_id, txid, crypto, COALESCE(amount_crypto, 0), COALESCE(amount_fiat, 0),
		COALESCE(need_more_confirmations, 1), COALESCE(callback_confirmed, 0), created_at, updated_at
		FROM %s WHERE crypto = ? AND txid = ? LIMIT 1`, s.table("transaction"))
	var t Transaction
	err := s.db.QueryRowContext(ctx, q, crypto, txid).Scan(&t.ID, &t.InvoiceID, &t.TxID, &t.Crypto, &t.AmountCrypto, &t.AmountFiat, &t.NeedMoreConfirmations, &t.CallbackConfirmed, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *Store) AddTransaction(ctx context.Context, t *Transaction) error {
	q := fmt.Sprintf(`INSERT INTO %s (invoice_id, txid, crypto, amount_crypto, amount_fiat, need_more_confirmations, callback_confirmed)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, s.table("transaction"))
	res, err := s.db.ExecContext(ctx, q, t.InvoiceID, t.TxID, t.Crypto, t.AmountCrypto, t.AmountFiat, t.NeedMoreConfirmations, t.CallbackConfirmed)
	if err != nil {
		return err
	}
	t.ID, err = res.LastInsertId()
	return err
}

func (s *Store) AddOutgoingTransaction(ctx context.Context, crypto, txid, addr string, amountCrypto, amountFiat decimal.Decimal) (Transaction, Invoice, bool, error) {
	if existing, err := s.ExistingTransactionByCryptoTxID(ctx, crypto, txid); err == nil {
		invoice, invoiceErr := s.InvoiceByID(ctx, existing.InvoiceID)
		return existing, invoice, true, invoiceErr
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, Invoice{}, false, err
	}
	invoice := Invoice{
		Crypto:        crypto,
		Addr:          addr,
		ExternalID:    "outgoing:" + txid,
		Fiat:          "USD",
		CallbackURL:   "",
		BalanceFiat:   amountFiat,
		BalanceCrypto: amountCrypto,
		AmountFiat:    amountFiat,
		AmountCrypto:  amountCrypto,
		ExchangeRate:  decimal.Zero,
		Status:        InvoiceOutgoing,
	}
	if !amountCrypto.IsZero() {
		invoice.ExchangeRate = amountFiat.Div(amountCrypto)
	}
	if err := s.CreateInvoice(ctx, &invoice); err != nil {
		return Transaction{}, Invoice{}, false, err
	}
	if err := s.AddInvoiceAddress(ctx, invoice.ID, crypto, addr); err != nil {
		return Transaction{}, Invoice{}, false, err
	}
	tx := Transaction{
		InvoiceID:             invoice.ID,
		TxID:                  txid,
		Crypto:                crypto,
		AmountCrypto:          amountCrypto,
		AmountFiat:            amountFiat,
		NeedMoreConfirmations: false,
		CallbackConfirmed:     true,
		Addr:                  addr,
	}
	if err := s.AddTransaction(ctx, &tx); err != nil {
		return Transaction{}, Invoice{}, false, err
	}
	tx.Invoice = &invoice
	return tx, invoice, false, nil
}

func (s *Store) DeleteUnconfirmed(ctx context.Context, crypto, txid string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE crypto = ? AND txid = ?", s.table("unconfirmed_transaction")), crypto, txid)
	return err
}

func (s *Store) AddUnconfirmed(ctx context.Context, tx *UnconfirmedTransaction) error {
	q := fmt.Sprintf(`INSERT INTO %s (invoice_id, addr, txid, crypto, amount_crypto, callback_confirmed)
		VALUES (?, ?, ?, ?, ?, ?)`, s.table("unconfirmed_transaction"))
	res, err := s.db.ExecContext(ctx, q, tx.InvoiceID, tx.Addr, tx.TxID, tx.Crypto, tx.AmountCrypto, tx.CallbackConfirmed)
	if err != nil {
		if isDuplicateSchemaError(err) || strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil
		}
		return err
	}
	tx.ID, err = res.LastInsertId()
	return err
}

func (s *Store) MarkTransactionCallbackConfirmed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET callback_confirmed = 1, updated_at = %s WHERE id = ?", s.table("transaction"), s.nowExpr()), id)
	return err
}

func (s *Store) MarkTransactionConfirmationsDone(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET need_more_confirmations = 0, updated_at = %s WHERE id = ?", s.table("transaction"), s.nowExpr()), id)
	return err
}

func (s *Store) MarkUnconfirmedCallbackConfirmed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET callback_confirmed = 1 WHERE id = ?", s.table("unconfirmed_transaction")), id)
	return err
}

func (s *Store) MarkNotificationCallbackConfirmed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET callback_confirmed = 1 WHERE id = ?", s.table("notification")), id)
	return err
}

func (s *Store) IncrementNotificationRetry(ctx context.Context, id int64, message string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET retries = retries + 1, message = ? WHERE id = ?", s.table("notification")), message, id)
	return err
}

func (s *Store) CreatePayout(ctx context.Context, p *Payout) error {
	q := fmt.Sprintf(`INSERT INTO %s (amount, crypto, dest_addr, callback_url, task_id, external_id, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, s.table("payout"))
	res, err := s.db.ExecContext(ctx, q, p.Amount, p.Crypto, p.DestAddr, nullOrString(p.CallbackURL), nullOrString(p.TaskID), nullOrString(p.ExternalID), p.Status)
	if err != nil {
		return err
	}
	p.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	return s.refreshOrderIndexForExternalID(ctx, nullStringValue(p.ExternalID))
}

func (s *Store) AddPayoutTx(ctx context.Context, payoutID int64, txid string) error {
	return s.AddPayoutTxDetail(ctx, payoutID, PayoutTx{TxID: txid, Status: PayoutInProgress, Kind: "payout"})
}

func (s *Store) AddPayoutTxDetail(ctx context.Context, payoutID int64, detail PayoutTx) error {
	detail.PayoutID = payoutID
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(payout_id, txid, status, kind, source_addr, dest_addr, amount, crypto, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			status = VALUES(status),
			kind = VALUES(kind),
			source_addr = VALUES(source_addr),
			dest_addr = VALUES(dest_addr),
			amount = VALUES(amount),
			crypto = VALUES(crypto),
			error = VALUES(error),
			updated_at = %s`, s.table("payout_tx"), s.nowExpr()),
		detail.PayoutID,
		nullOrText(detail.TxID),
		firstNonEmptyString(strings.ToUpper(strings.TrimSpace(detail.Status)), PayoutInProgress),
		firstNonEmptyString(strings.ToLower(strings.TrimSpace(detail.Kind)), "payout"),
		nullOrText(detail.SourceAddr),
		nullOrText(detail.DestAddr),
		detail.Amount,
		nullOrText(strings.ToUpper(strings.TrimSpace(detail.Crypto))),
		nullOrText(detail.Error),
	)
	return err
}

func (s *Store) SetPayoutFee(ctx context.Context, payoutID int64, fee decimal.Decimal, asset string) error {
	if !fee.GreaterThanOrEqual(decimal.Zero) || strings.TrimSpace(asset) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET fee = ?, fee_asset = ?, updated_at = %s WHERE id = ?", s.table("payout"), s.nowExpr()), fee, strings.ToUpper(strings.TrimSpace(asset)), payoutID)
	return err
}

func (s *Store) PayoutFee(ctx context.Context, payoutID int64) (decimal.Decimal, string, error) {
	var fee decimal.Decimal
	var asset string
	err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COALESCE(fee, 0), COALESCE(fee_asset, '') FROM %s WHERE id = ? LIMIT 1", s.table("payout")), payoutID).Scan(&fee, &asset)
	return fee, asset, err
}

func (s *Store) PayoutByExternalID(ctx context.Context, crypto, externalID string) (Payout, error) {
	q := fmt.Sprintf(`SELECT id, created_at, updated_at, COALESCE(amount, 0), COALESCE(crypto, ''), COALESCE(dest_addr, ''),
		success, error, callback_url, task_id, external_id, COALESCE(status, 'IN_PROGRESS')
		FROM %s WHERE crypto = ? AND external_id = ? LIMIT 1`, s.table("payout"))
	var p Payout
	err := s.db.QueryRowContext(ctx, q, crypto, externalID).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.Amount, &p.Crypto, &p.DestAddr, &p.Success, &p.Error, &p.CallbackURL, &p.TaskID, &p.ExternalID, &p.Status)
	if err != nil {
		return p, err
	}
	p.Transactions, err = s.PayoutTxs(ctx, p.ID)
	return p, err
}

func (s *Store) PayoutTxs(ctx context.Context, payoutID int64) ([]PayoutTx, error) {
	grouped, err := s.PayoutTxsByPayoutIDs(ctx, []int64{payoutID})
	if err != nil {
		return nil, err
	}
	return grouped[payoutID], nil
}

func (s *Store) PayoutTxsByPayoutIDs(ctx context.Context, payoutIDs []int64) (map[int64][]PayoutTx, error) {
	out := make(map[int64][]PayoutTx, len(payoutIDs))
	if len(payoutIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, payout_id, created_at, updated_at,
		COALESCE(txid, ''), COALESCE(status, 'IN_PROGRESS'), COALESCE(kind, 'payout'),
		COALESCE(source_addr, ''), COALESCE(dest_addr, ''), COALESCE(amount, 0), COALESCE(crypto, ''), COALESCE(error, '')
		FROM %s WHERE payout_id IN (%s) ORDER BY payout_id, id`, s.table("payout_tx"), placeholders(len(payoutIDs))), int64Args(payoutIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tx PayoutTx
		if err := rows.Scan(&tx.ID, &tx.PayoutID, &tx.CreatedAt, &tx.UpdatedAt, &tx.TxID, &tx.Status, &tx.Kind, &tx.SourceAddr, &tx.DestAddr, &tx.Amount, &tx.Crypto, &tx.Error); err != nil {
			return nil, err
		}
		out[tx.PayoutID] = append(out[tx.PayoutID], tx)
	}
	return out, rows.Err()
}

func nullOrString(v sql.NullString) any {
	if v.Valid {
		return v.String
	}
	return nil
}

func nullOrText(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func serverHostSettingName(crypto string) string {
	return "server_host:" + strings.ToUpper(strings.TrimSpace(crypto))
}

func limitOffset(limit, offset int) (int, int) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func dateOrZero(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func int64Args(values []int64) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func stringArgs(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
