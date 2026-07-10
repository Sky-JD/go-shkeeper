package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

func invoiceRequestKey(externalID, callbackURL, fiat string) []byte {
	value := strings.TrimSpace(externalID) + "\x00" + strings.TrimSpace(callbackURL) + "\x00" + strings.ToUpper(strings.TrimSpace(fiat))
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func (s *Store) InvoiceByIdempotencyKey(ctx context.Context, key []byte) (Invoice, error) {
	q := fmt.Sprintf(`SELECT id, crypto, COALESCE(addr, ''), COALESCE(external_id, ''), COALESCE(fiat, ''), COALESCE(callback_url, ''),
		COALESCE(balance_fiat, 0), COALESCE(balance_crypto, 0), COALESCE(amount_fiat, 0), COALESCE(amount_crypto, 0),
		COALESCE(exchange_rate, 0), COALESCE(status, 'UNPAID'), created_at, updated_at
		FROM %s WHERE idempotency_key = ? LIMIT 1`, s.table("invoice"))
	return s.scanInvoice(ctx, q, key)
}

func (s *Store) CreateInvoiceWithAddress(ctx context.Context, invoice *Invoice, crypto, addr string, key []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := fmt.Sprintf(`INSERT INTO %s
		(crypto, addr, external_id, fiat, callback_url, balance_fiat, balance_crypto, amount_fiat, amount_crypto, exchange_rate, status, idempotency_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.table("invoice"))
	res, err := tx.ExecContext(ctx, q,
		invoice.Crypto,
		invoice.Addr,
		invoice.ExternalID,
		invoice.Fiat,
		invoice.CallbackURL,
		invoice.BalanceFiat,
		invoice.BalanceCrypto,
		invoice.AmountFiat,
		invoice.AmountCrypto,
		invoice.ExchangeRate,
		invoice.Status,
		key,
	)
	if err != nil {
		return err
	}
	invoice.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (invoice_id, crypto, addr) VALUES (?, ?, ?)", s.table("invoice_address")), invoice.ID, crypto, addr); err != nil {
		return err
	}
	if err := s.refreshOrderIndexForExternalIDWith(ctx, tx, invoice.ExternalID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetInvoiceIdempotencyKey(ctx context.Context, invoiceID int64, key []byte) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET idempotency_key = COALESCE(idempotency_key, ?) WHERE id = ?", s.table("invoice")), key, invoiceID)
	return err
}

func (s *Store) ApplyConfirmedTransaction(ctx context.Context, incoming *Transaction, llimit, ulimit decimal.Decimal) (Invoice, bool, error) {
	dbtx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Invoice{}, false, err
	}
	defer dbtx.Rollback()

	invoice, err := s.invoiceByIDForUpdate(ctx, dbtx, incoming.InvoiceID)
	if err != nil {
		return Invoice{}, false, err
	}

	existing, err := existingTransactionWith(ctx, dbtx, incoming.Crypto, incoming.TxID, invoice.ID)
	if err == nil {
		if err := dbtx.Commit(); err != nil {
			return Invoice{}, false, err
		}
		*incoming = existing
		incoming.Invoice = &invoice
		return invoice, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Invoice{}, false, err
	}

	q := fmt.Sprintf(`INSERT INTO %s (invoice_id, txid, crypto, amount_crypto, amount_fiat, need_more_confirmations, callback_confirmed)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, s.table("transaction"))
	res, err := dbtx.ExecContext(ctx, q,
		invoice.ID,
		incoming.TxID,
		incoming.Crypto,
		incoming.AmountCrypto,
		incoming.AmountFiat,
		incoming.NeedMoreConfirmations,
		incoming.CallbackConfirmed,
	)
	if err != nil {
		return Invoice{}, false, err
	}
	incoming.ID, err = res.LastInsertId()
	if err != nil {
		return Invoice{}, false, err
	}

	newFiat := invoice.BalanceFiat.Add(incoming.AmountFiat)
	newCrypto := invoice.BalanceCrypto
	if incoming.Crypto == invoice.Crypto {
		newCrypto = newCrypto.Add(incoming.AmountCrypto)
	}
	status := invoiceStatus(newFiat, invoice.AmountFiat, llimit, ulimit)
	if _, err := dbtx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s
		SET balance_fiat = ?, balance_crypto = ?, status = ?, updated_at = %s
		WHERE id = ?`, s.table("invoice"), s.nowExpr()), newFiat, newCrypto, status, invoice.ID); err != nil {
		return Invoice{}, false, err
	}
	if _, err := dbtx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE crypto = ? AND txid = ?", s.table("unconfirmed_transaction")), incoming.Crypto, incoming.TxID); err != nil {
		return Invoice{}, false, err
	}
	if err := s.refreshOrderIndexForExternalIDWith(ctx, dbtx, invoice.ExternalID); err != nil {
		return Invoice{}, false, err
	}
	if err := dbtx.Commit(); err != nil {
		return Invoice{}, false, err
	}

	invoice.BalanceFiat = newFiat
	invoice.BalanceCrypto = newCrypto
	invoice.Status = status
	incoming.Invoice = &invoice
	return invoice, false, nil
}

func (s *Store) invoiceByIDForUpdate(ctx context.Context, tx *sql.Tx, invoiceID int64) (Invoice, error) {
	q := fmt.Sprintf(`SELECT id, crypto, COALESCE(addr, ''), COALESCE(external_id, ''), COALESCE(fiat, ''), COALESCE(callback_url, ''),
		COALESCE(balance_fiat, 0), COALESCE(balance_crypto, 0), COALESCE(amount_fiat, 0), COALESCE(amount_crypto, 0),
		COALESCE(exchange_rate, 0), COALESCE(status, 'UNPAID'), created_at, updated_at
		FROM %s WHERE id = ? LIMIT 1 FOR UPDATE`, s.table("invoice"))
	var invoice Invoice
	err := tx.QueryRowContext(ctx, q, invoiceID).Scan(
		&invoice.ID,
		&invoice.Crypto,
		&invoice.Addr,
		&invoice.ExternalID,
		&invoice.Fiat,
		&invoice.CallbackURL,
		&invoice.BalanceFiat,
		&invoice.BalanceCrypto,
		&invoice.AmountFiat,
		&invoice.AmountCrypto,
		&invoice.ExchangeRate,
		&invoice.Status,
		&invoice.CreatedAt,
		&invoice.UpdatedAt,
	)
	return invoice, err
}

type contextQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func existingTransactionWith(ctx context.Context, db contextQueryRower, crypto, txid string, invoiceID int64) (Transaction, error) {
	q := `SELECT id, invoice_id, txid, crypto, COALESCE(amount_crypto, 0), COALESCE(amount_fiat, 0),
		COALESCE(need_more_confirmations, 1), COALESCE(callback_confirmed, 0), created_at, updated_at
		FROM ` + "`transaction`" + ` WHERE crypto = ? AND txid = ? AND invoice_id = ? LIMIT 1`
	var transaction Transaction
	err := db.QueryRowContext(ctx, q, crypto, txid, invoiceID).Scan(
		&transaction.ID,
		&transaction.InvoiceID,
		&transaction.TxID,
		&transaction.Crypto,
		&transaction.AmountCrypto,
		&transaction.AmountFiat,
		&transaction.NeedMoreConfirmations,
		&transaction.CallbackConfirmed,
		&transaction.CreatedAt,
		&transaction.UpdatedAt,
	)
	return transaction, err
}
