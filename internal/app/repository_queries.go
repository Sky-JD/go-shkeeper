package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) ListInvoices(ctx context.Context, externalID string, filter OrderFilter) ([]InvoiceDetail, error) {
	limit, offset := limitOffset(filter.Limit, filter.Offset)
	where := []string{"1 = 1"}
	args := []any{}
	if externalID != "" {
		where = append(where, "external_id = ?")
		args = append(args, externalID)
	}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, strings.ToUpper(filter.Status))
	}
	if filter.Crypto != "" {
		where = append(where, "crypto = ?")
		args = append(args, strings.ToUpper(filter.Crypto))
	}
	if t, ok := dateOrZero(filter.FromDate); ok {
		where = append(where, "created_at >= ?")
		args = append(args, t)
	}
	if t, ok := dateOrZero(filter.ToDate); ok {
		where = append(where, "created_at <= ?")
		args = append(args, t)
	}
	args = append(args, limit, offset)
	q := fmt.Sprintf(`SELECT id, crypto, COALESCE(addr, ''), COALESCE(external_id, ''), COALESCE(fiat, ''), COALESCE(callback_url, ''),
		COALESCE(balance_fiat, 0), COALESCE(balance_crypto, 0), COALESCE(amount_fiat, 0), COALESCE(amount_crypto, 0),
		COALESCE(exchange_rate, 0), COALESCE(status, 'UNPAID'), created_at, updated_at
		FROM %s WHERE %s ORDER BY id DESC LIMIT ? OFFSET ?`, s.table("invoice"), strings.Join(where, " AND "))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invoices []Invoice
	for rows.Next() {
		var i Invoice
		if err := rows.Scan(&i.ID, &i.Crypto, &i.Addr, &i.ExternalID, &i.Fiat, &i.CallbackURL, &i.BalanceFiat, &i.BalanceCrypto, &i.AmountFiat, &i.AmountCrypto, &i.ExchangeRate, &i.Status, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		invoices = append(invoices, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.InvoiceDetails(ctx, invoices)
}

func (s *Store) InvoicesByExternalIDs(ctx context.Context, externalIDs []string, filter OrderFilter) (map[string][]InvoiceDetail, error) {
	externalIDs = uniqueNonEmptyStrings(externalIDs)
	out := make(map[string][]InvoiceDetail, len(externalIDs))
	if len(externalIDs) == 0 {
		return out, nil
	}
	where := []string{"external_id IN (" + placeholders(len(externalIDs)) + ")"}
	args := stringArgs(externalIDs)
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, strings.ToUpper(filter.Status))
	}
	if filter.Crypto != "" {
		where = append(where, "crypto = ?")
		args = append(args, strings.ToUpper(filter.Crypto))
	}
	if t, ok := dateOrZero(filter.FromDate); ok {
		where = append(where, "created_at >= ?")
		args = append(args, t)
	}
	if t, ok := dateOrZero(filter.ToDate); ok {
		where = append(where, "created_at <= ?")
		args = append(args, t)
	}
	q := fmt.Sprintf(`SELECT id, crypto, COALESCE(addr, ''), COALESCE(external_id, ''), COALESCE(fiat, ''), COALESCE(callback_url, ''),
		COALESCE(balance_fiat, 0), COALESCE(balance_crypto, 0), COALESCE(amount_fiat, 0), COALESCE(amount_crypto, 0),
		COALESCE(exchange_rate, 0), COALESCE(status, 'UNPAID'), created_at, updated_at
		FROM %s WHERE %s ORDER BY external_id, id DESC`, s.table("invoice"), strings.Join(where, " AND "))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	invoices := make([]Invoice, 0)
	for rows.Next() {
		var i Invoice
		if err := rows.Scan(&i.ID, &i.Crypto, &i.Addr, &i.ExternalID, &i.Fiat, &i.CallbackURL, &i.BalanceFiat, &i.BalanceCrypto, &i.AmountFiat, &i.AmountCrypto, &i.ExchangeRate, &i.Status, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		invoices = append(invoices, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	details, err := s.InvoiceDetails(ctx, invoices)
	if err != nil {
		return nil, err
	}
	for _, detail := range details {
		out[detail.Invoice.ExternalID] = append(out[detail.Invoice.ExternalID], detail)
	}
	return out, nil
}

func (s *Store) InvoiceDetail(ctx context.Context, invoice Invoice) (InvoiceDetail, error) {
	details, err := s.InvoiceDetails(ctx, []Invoice{invoice})
	if err != nil {
		return InvoiceDetail{}, err
	}
	if len(details) == 0 {
		return InvoiceDetail{Invoice: invoice}, nil
	}
	return details[0], nil
}

func (s *Store) InvoiceDetails(ctx context.Context, invoices []Invoice) ([]InvoiceDetail, error) {
	if len(invoices) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(invoices))
	index := make(map[int64]int, len(invoices))
	out := make([]InvoiceDetail, len(invoices))
	for i, invoice := range invoices {
		ids = append(ids, invoice.ID)
		index[invoice.ID] = i
		out[i].Invoice = invoice
	}
	addresses, err := s.InvoiceAddressesByInvoiceIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for invoiceID, rows := range addresses {
		out[index[invoiceID]].Addresses = rows
	}
	txs, err := s.InvoiceTransactionsByInvoiceIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for invoiceID, rows := range txs {
		out[index[invoiceID]].Transactions = rows
	}
	utxs, err := s.InvoiceUnconfirmedTransactionsByInvoiceIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for invoiceID, rows := range utxs {
		out[index[invoiceID]].UnconfirmedTXs = rows
	}
	return out, nil
}

func (s *Store) InvoiceAddresses(ctx context.Context, invoiceID int64) ([]InvoiceAddress, error) {
	grouped, err := s.InvoiceAddressesByInvoiceIDs(ctx, []int64{invoiceID})
	if err != nil {
		return nil, err
	}
	return grouped[invoiceID], nil
}

func (s *Store) InvoiceAddressesByInvoiceIDs(ctx context.Context, invoiceIDs []int64) (map[int64][]InvoiceAddress, error) {
	out := make(map[int64][]InvoiceAddress, len(invoiceIDs))
	if len(invoiceIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT id, invoice_id, COALESCE(crypto, ''), COALESCE(addr, ''), created_at FROM %s WHERE invoice_id IN (%s) ORDER BY invoice_id, id", s.table("invoice_address"), placeholders(len(invoiceIDs))), int64Args(invoiceIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a InvoiceAddress
		if err := rows.Scan(&a.ID, &a.InvoiceID, &a.Crypto, &a.Addr, &a.CreatedAt); err != nil {
			return nil, err
		}
		out[a.InvoiceID] = append(out[a.InvoiceID], a)
	}
	return out, rows.Err()
}

func (s *Store) InvoiceTransactions(ctx context.Context, invoiceID int64) ([]Transaction, error) {
	grouped, err := s.InvoiceTransactionsByInvoiceIDs(ctx, []int64{invoiceID})
	if err != nil {
		return nil, err
	}
	return grouped[invoiceID], nil
}

func (s *Store) InvoiceTransactionsByInvoiceIDs(ctx context.Context, invoiceIDs []int64) (map[int64][]Transaction, error) {
	out := make(map[int64][]Transaction, len(invoiceIDs))
	if len(invoiceIDs) == 0 {
		return out, nil
	}
	q := fmt.Sprintf(`SELECT t.id, t.invoice_id, COALESCE(t.txid, ''), COALESCE(t.crypto, ''), COALESCE(t.amount_crypto, 0), COALESCE(t.amount_fiat, 0),
		COALESCE(t.need_more_confirmations, 1), COALESCE(t.callback_confirmed, 0), t.created_at, t.updated_at,
		COALESCE(ia.addr, i.addr, '')
		FROM %s t JOIN %s i ON i.id = t.invoice_id
		LEFT JOIN %s ia ON ia.invoice_id = t.invoice_id AND ia.crypto = t.crypto
		WHERE t.invoice_id IN (%s) ORDER BY t.invoice_id, t.id`, s.table("transaction"), s.table("invoice"), s.table("invoice_address"), placeholders(len(invoiceIDs)))
	rows, err := s.db.QueryContext(ctx, q, int64Args(invoiceIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.InvoiceID, &t.TxID, &t.Crypto, &t.AmountCrypto, &t.AmountFiat, &t.NeedMoreConfirmations, &t.CallbackConfirmed, &t.CreatedAt, &t.UpdatedAt, &t.Addr); err != nil {
			return nil, err
		}
		out[t.InvoiceID] = append(out[t.InvoiceID], t)
	}
	return out, rows.Err()
}

func (s *Store) InvoiceUnconfirmedTransactions(ctx context.Context, invoiceID int64) ([]UnconfirmedTransaction, error) {
	grouped, err := s.InvoiceUnconfirmedTransactionsByInvoiceIDs(ctx, []int64{invoiceID})
	if err != nil {
		return nil, err
	}
	return grouped[invoiceID], nil
}

func (s *Store) InvoiceUnconfirmedTransactionsByInvoiceIDs(ctx context.Context, invoiceIDs []int64) (map[int64][]UnconfirmedTransaction, error) {
	out := make(map[int64][]UnconfirmedTransaction, len(invoiceIDs))
	if len(invoiceIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, invoice_id, COALESCE(addr, ''), COALESCE(txid, ''), COALESCE(crypto, ''),
		COALESCE(amount_crypto, 0), COALESCE(callback_confirmed, 0), created_at
		FROM %s WHERE invoice_id IN (%s) ORDER BY invoice_id, id`, s.table("unconfirmed_transaction"), placeholders(len(invoiceIDs))), int64Args(invoiceIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tx UnconfirmedTransaction
		if err := rows.Scan(&tx.ID, &tx.InvoiceID, &tx.Addr, &tx.TxID, &tx.Crypto, &tx.AmountCrypto, &tx.CallbackConfirmed, &tx.CreatedAt); err != nil {
			return nil, err
		}
		out[tx.InvoiceID] = append(out[tx.InvoiceID], tx)
	}
	return out, rows.Err()
}

func (s *Store) ListTransactions(ctx context.Context, crypto, addr string) ([]Transaction, error) {
	var q string
	var args []any
	if crypto == "" || addr == "" {
		q = fmt.Sprintf(`SELECT t.id, t.invoice_id, COALESCE(t.txid, ''), COALESCE(t.crypto, ''), COALESCE(t.amount_crypto, 0), COALESCE(t.amount_fiat, 0),
			COALESCE(t.need_more_confirmations, 1), COALESCE(t.callback_confirmed, 0), t.created_at, t.updated_at,
			COALESCE(ia.addr, i.addr, '')
			FROM %s t JOIN %s i ON i.id = t.invoice_id
			LEFT JOIN %s ia ON ia.invoice_id = t.invoice_id AND ia.crypto = t.crypto
			ORDER BY t.id DESC LIMIT 1000`, s.table("transaction"), s.table("invoice"), s.table("invoice_address"))
	} else {
		q = fmt.Sprintf(`SELECT t.id, t.invoice_id, COALESCE(t.txid, ''), COALESCE(t.crypto, ''), COALESCE(t.amount_crypto, 0), COALESCE(t.amount_fiat, 0),
			COALESCE(t.need_more_confirmations, 1), COALESCE(t.callback_confirmed, 0), t.created_at, t.updated_at,
			COALESCE(ia.addr, i.addr, '')
			FROM %s t JOIN %s i ON i.id = t.invoice_id
			LEFT JOIN %s ia ON ia.invoice_id = t.invoice_id AND ia.crypto = t.crypto
			WHERE t.crypto = ? AND (i.addr = ? OR ia.addr = ?)
			ORDER BY t.id DESC LIMIT 1000`, s.table("transaction"), s.table("invoice"), s.table("invoice_address"))
		args = []any{strings.ToUpper(crypto), addr, addr}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.InvoiceID, &t.TxID, &t.Crypto, &t.AmountCrypto, &t.AmountFiat, &t.NeedMoreConfirmations, &t.CallbackConfirmed, &t.CreatedAt, &t.UpdatedAt, &t.Addr); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) ListUnconfirmedTransactions(ctx context.Context, crypto, addr string) ([]UnconfirmedTransaction, error) {
	where := ""
	args := []any{}
	if crypto != "" && addr != "" {
		where = "WHERE crypto = ? AND addr = ?"
		args = []any{strings.ToUpper(crypto), addr}
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, invoice_id, COALESCE(addr, ''), COALESCE(txid, ''), COALESCE(crypto, ''),
		COALESCE(amount_crypto, 0), COALESCE(callback_confirmed, 0), created_at FROM %s %s ORDER BY id DESC LIMIT 1000`, s.table("unconfirmed_transaction"), where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnconfirmedTransaction
	for rows.Next() {
		var tx UnconfirmedTransaction
		if err := rows.Scan(&tx.ID, &tx.InvoiceID, &tx.Addr, &tx.TxID, &tx.Crypto, &tx.AmountCrypto, &tx.CallbackConfirmed, &tx.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (s *Store) TxInfo(ctx context.Context, txid, externalID string) (map[string]string, error) {
	q := fmt.Sprintf(`SELECT t.crypto, COALESCE(t.amount_fiat, 0), COALESCE(ia.addr, i.addr, '')
		FROM %s t JOIN %s i ON i.id = t.invoice_id
		LEFT JOIN %s ia ON ia.invoice_id = t.invoice_id AND ia.crypto = t.crypto
		WHERE t.txid = ? AND i.external_id = ? LIMIT 1`, s.table("transaction"), s.table("invoice"), s.table("invoice_address"))
	var crypto, addr string
	var amount string
	err := s.db.QueryRowContext(ctx, q, txid, externalID).Scan(&crypto, &amount, &addr)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]string{"crypto": crypto, "amount": amount, "addr": addr}, nil
}

func (s *Store) GetOrder(ctx context.Context, externalID string) (OrderRecord, error) {
	grouped, err := s.InvoicesByExternalIDs(ctx, []string{externalID}, OrderFilter{})
	if err != nil {
		return OrderRecord{}, err
	}
	payouts, err := s.PayoutsByExternalID(ctx, externalID)
	if err != nil {
		return OrderRecord{}, err
	}
	return OrderRecord{ExternalID: externalID, Invoices: grouped[externalID], Payouts: payouts}, nil
}

func (s *Store) ListOrders(ctx context.Context, filter OrderFilter) ([]OrderRecord, error) {
	page, err := s.ListOrdersPage(ctx, filter)
	if err != nil {
		return nil, err
	}
	return page.Orders, nil
}

func (s *Store) ListOrdersPage(ctx context.Context, filter OrderFilter) (OrderListPage, error) {
	externalRows, err := s.ListOrderExternalIDRows(ctx, filter, true)
	if err != nil {
		return OrderListPage{}, err
	}
	limit, _ := limitOffset(filter.Limit, filter.Offset)
	nextCursor := ""
	if len(externalRows) > limit {
		nextCursor = encodeOrderCursor(externalRows[limit-1])
		externalRows = externalRows[:limit]
	}
	externalIDs := make([]string, 0, len(externalRows))
	for _, row := range externalRows {
		externalIDs = append(externalIDs, row.ExternalID)
	}
	invoicesByExternal, err := s.InvoicesByExternalIDs(ctx, externalIDs, OrderFilter{})
	if err != nil {
		return OrderListPage{}, err
	}
	payoutsByExternal, err := s.PayoutsByExternalIDs(ctx, externalIDs)
	if err != nil {
		return OrderListPage{}, err
	}
	out := make([]OrderRecord, 0, len(externalIDs))
	for _, externalID := range externalIDs {
		out = append(out, OrderRecord{
			ExternalID: externalID,
			Invoices:   invoicesByExternal[externalID],
			Payouts:    payoutsByExternal[externalID],
		})
	}
	return OrderListPage{Orders: out, NextCursor: nextCursor}, nil
}

func (s *Store) ListOrderExternalIDs(ctx context.Context, filter OrderFilter) ([]string, error) {
	rows, err := s.ListOrderExternalIDRows(ctx, filter, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ExternalID)
	}
	return out, nil
}

func (s *Store) ListOrderExternalIDRows(ctx context.Context, filter OrderFilter, lookahead bool) ([]OrderCursorRow, error) {
	limit, offset := limitOffset(filter.Limit, filter.Offset)
	if lookahead {
		limit++
	}
	cursor, hasCursor, err := decodeOrderCursor(filter.Cursor)
	if err != nil {
		return nil, err
	}
	if hasCursor {
		offset = 0
	}
	where := []string{"oi.external_id <> ''"}
	args := []any{}
	if filter.ExternalID != "" {
		where = append(where, "oi.external_id = ?")
		args = append(args, filter.ExternalID)
	}
	if sourceWhere, sourceArgs := s.orderSourceFilter(filter); sourceWhere != "" {
		where = append(where, sourceWhere)
		args = append(args, sourceArgs...)
	}
	if hasCursor {
		where = append(where, "(oi.sort_at < ? OR (oi.sort_at = ? AND oi.external_id < ?))")
		args = append(args, cursor.SortAt, cursor.SortAt, cursor.ExternalID)
	}
	args = append(args, limit)
	offsetSQL := ""
	if offset > 0 {
		offsetSQL = " OFFSET ?"
		args = append(args, offset)
	}
	q := fmt.Sprintf(`SELECT oi.external_id, oi.sort_at
		FROM %s oi
		WHERE %s
		ORDER BY oi.sort_at DESC, oi.external_id DESC
		LIMIT ?%s`, s.table("order_index"), strings.Join(where, " AND "), offsetSQL)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]OrderCursorRow, 0, limit)
	for rows.Next() {
		var row OrderCursorRow
		if err := rows.Scan(&row.ExternalID, &row.SortAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) orderSourceFilter(filter OrderFilter) (string, []any) {
	hasFilter := filter.Status != "" || filter.Crypto != ""
	fromDate, hasFromDate := dateOrZero(filter.FromDate)
	toDate, hasToDate := dateOrZero(filter.ToDate)
	hasFilter = hasFilter || hasFromDate || hasToDate
	if !hasFilter {
		return "", nil
	}
	sourceWhere := func(alias string) ([]string, []any) {
		where := []string{alias + ".external_id = oi.external_id"}
		args := []any{}
		if filter.Status != "" {
			where = append(where, alias+".status = ?")
			args = append(args, strings.ToUpper(filter.Status))
		}
		if filter.Crypto != "" {
			where = append(where, alias+".crypto = ?")
			args = append(args, strings.ToUpper(filter.Crypto))
		}
		if hasFromDate {
			where = append(where, alias+".created_at >= ?")
			args = append(args, fromDate)
		}
		if hasToDate {
			where = append(where, alias+".created_at <= ?")
			args = append(args, toDate)
		}
		return where, args
	}
	invoiceWhere, invoiceArgs := sourceWhere("i")
	payoutWhere, payoutArgs := sourceWhere("p")
	args := append(invoiceArgs, payoutArgs...)
	return fmt.Sprintf(`(EXISTS (SELECT 1 FROM %s i WHERE %s LIMIT 1)
		OR EXISTS (SELECT 1 FROM %s p WHERE %s LIMIT 1))`,
		s.table("invoice"), strings.Join(invoiceWhere, " AND "),
		s.table("payout"), strings.Join(payoutWhere, " AND ")), args
}

func (s *Store) PayoutsByExternalID(ctx context.Context, externalID string) ([]Payout, error) {
	if externalID == "" {
		return nil, nil
	}
	grouped, err := s.PayoutsByExternalIDs(ctx, []string{externalID})
	if err != nil {
		return nil, err
	}
	return grouped[externalID], nil
}

func (s *Store) PayoutsByExternalIDs(ctx context.Context, externalIDs []string) (map[string][]Payout, error) {
	externalIDs = uniqueNonEmptyStrings(externalIDs)
	out := make(map[string][]Payout, len(externalIDs))
	if len(externalIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, created_at, updated_at, COALESCE(amount, 0), COALESCE(crypto, ''), COALESCE(dest_addr, ''),
		success, error, callback_url, task_id, external_id, COALESCE(status, 'IN_PROGRESS')
		FROM %s WHERE external_id IN (%s) ORDER BY external_id, id DESC`, s.table("payout"), placeholders(len(externalIDs))), stringArgs(externalIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var payouts []Payout
	payoutIDs := make([]int64, 0)
	for rows.Next() {
		var p Payout
		if err := rows.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.Amount, &p.Crypto, &p.DestAddr, &p.Success, &p.Error, &p.CallbackURL, &p.TaskID, &p.ExternalID, &p.Status); err != nil {
			return nil, err
		}
		payoutIDs = append(payoutIDs, p.ID)
		payouts = append(payouts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	txs, err := s.PayoutTxsByPayoutIDs(ctx, payoutIDs)
	if err != nil {
		return nil, err
	}
	for _, payout := range payouts {
		payout.Transactions = txs[payout.ID]
		out[nullStringValue(payout.ExternalID)] = append(out[nullStringValue(payout.ExternalID)], payout)
	}
	return out, nil
}

func (s *Store) PayoutsByCryptoAmount(ctx context.Context, crypto string, amount string) ([]Payout, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, created_at, updated_at, COALESCE(amount, 0), COALESCE(crypto, ''), COALESCE(dest_addr, ''),
		success, error, callback_url, task_id, external_id, COALESCE(status, 'IN_PROGRESS')
		FROM %s WHERE crypto = ? AND amount = ? ORDER BY id DESC LIMIT 200`, s.table("payout")), crypto, amount)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Payout, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var p Payout
		if err := rows.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.Amount, &p.Crypto, &p.DestAddr, &p.Success, &p.Error, &p.CallbackURL, &p.TaskID, &p.ExternalID, &p.Status); err != nil {
			return nil, err
		}
		ids = append(ids, p.ID)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	txs, err := s.PayoutTxsByPayoutIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Transactions = txs[out[i].ID]
	}
	return out, nil
}

func (s *Store) ListPayouts(ctx context.Context, crypto, status, destAddr, txid string, limit int) ([]Payout, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	where := []string{"1 = 1"}
	args := []any{}
	if crypto != "" {
		where = append(where, "p.crypto = ?")
		args = append(args, strings.ToUpper(crypto))
	}
	if status != "" {
		where = append(where, "p.status = ?")
		args = append(args, strings.ToUpper(status))
	}
	if destAddr != "" {
		where = append(where, "p.dest_addr LIKE ?")
		args = append(args, "%"+destAddr+"%")
	}
	join := ""
	if txid != "" {
		join = " JOIN " + s.table("payout_tx") + " pt_filter ON pt_filter.payout_id = p.id"
		where = append(where, "pt_filter.txid LIKE ?")
		args = append(args, "%"+txid+"%")
	}
	args = append(args, limit)
	q := fmt.Sprintf(`SELECT p.id, p.created_at, p.updated_at, COALESCE(p.amount, 0), COALESCE(p.crypto, ''), COALESCE(p.dest_addr, ''),
		p.success, p.error, p.callback_url, p.task_id, p.external_id, COALESCE(p.status, 'IN_PROGRESS')
		FROM %s p%s WHERE %s ORDER BY p.id DESC LIMIT ?`, s.table("payout"), join, strings.Join(where, " AND "))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Payout, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var p Payout
		if err := rows.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.Amount, &p.Crypto, &p.DestAddr, &p.Success, &p.Error, &p.CallbackURL, &p.TaskID, &p.ExternalID, &p.Status); err != nil {
			return nil, err
		}
		ids = append(ids, p.ID)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	txs, err := s.PayoutTxsByPayoutIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Transactions = txs[out[i].ID]
	}
	return out, nil
}

func (s *Store) ListExchangeRates(ctx context.Context, fiat string) ([]ExchangeRate, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, COALESCE(source, 'dynamic'), COALESCE(crypto, ''), COALESCE(fiat, ''),
		COALESCE(rate, 0), COALESCE(fee, 2), COALESCE(fixed_fee, 0), COALESCE(fee_policy, 'PERCENT_FEE')
		FROM %s WHERE fiat = ? ORDER BY crypto`, s.table("exchange_rate")), strings.ToUpper(fiat))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ExchangeRate, 0)
	for rows.Next() {
		var rate ExchangeRate
		if err := rows.Scan(&rate.ID, &rate.Source, &rate.Crypto, &rate.Fiat, &rate.Rate, &rate.Fee, &rate.FixedFee, &rate.FeePolicy); err != nil {
			return nil, err
		}
		out = append(out, rate)
	}
	return out, rows.Err()
}

func (s *Store) PendingCallbackTransactions(ctx context.Context) ([]Transaction, error) {
	q := fmt.Sprintf(`SELECT t.id, t.invoice_id, COALESCE(t.txid, ''), COALESCE(t.crypto, ''), COALESCE(t.amount_crypto, 0), COALESCE(t.amount_fiat, 0),
		COALESCE(t.need_more_confirmations, 1), COALESCE(t.callback_confirmed, 0), t.created_at, t.updated_at,
		COALESCE(ia.addr, i.addr, '')
		FROM %s t JOIN %s i ON i.id = t.invoice_id
		LEFT JOIN %s ia ON ia.invoice_id = t.invoice_id AND ia.crypto = t.crypto
		WHERE t.callback_confirmed = 0 AND t.need_more_confirmations = 0
		ORDER BY t.id LIMIT 200`, s.table("transaction"), s.table("invoice"), s.table("invoice_address"))
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.InvoiceID, &t.TxID, &t.Crypto, &t.AmountCrypto, &t.AmountFiat, &t.NeedMoreConfirmations, &t.CallbackConfirmed, &t.CreatedAt, &t.UpdatedAt, &t.Addr); err != nil {
			return nil, err
		}
		inv, err := s.InvoiceByID(ctx, t.InvoiceID)
		if err != nil {
			return nil, err
		}
		t.Invoice = &inv
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) PendingConfirmationTransactions(ctx context.Context) ([]Transaction, error) {
	q := fmt.Sprintf(`SELECT id, invoice_id, COALESCE(txid, ''), COALESCE(crypto, ''), COALESCE(amount_crypto, 0), COALESCE(amount_fiat, 0),
		COALESCE(need_more_confirmations, 1), COALESCE(callback_confirmed, 0), created_at, updated_at
		FROM %s WHERE callback_confirmed = 0 AND need_more_confirmations = 1 ORDER BY id LIMIT 200`, s.table("transaction"))
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.InvoiceID, &t.TxID, &t.Crypto, &t.AmountCrypto, &t.AmountFiat, &t.NeedMoreConfirmations, &t.CallbackConfirmed, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) PendingUnconfirmedCallbacks(ctx context.Context) ([]UnconfirmedTransaction, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, invoice_id, COALESCE(addr, ''), COALESCE(txid, ''), COALESCE(crypto, ''),
		COALESCE(amount_crypto, 0), COALESCE(callback_confirmed, 0), created_at
		FROM %s WHERE callback_confirmed = 0 ORDER BY id LIMIT 200`, s.table("unconfirmed_transaction")))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnconfirmedTransaction
	for rows.Next() {
		var tx UnconfirmedTransaction
		if err := rows.Scan(&tx.ID, &tx.InvoiceID, &tx.Addr, &tx.TxID, &tx.Crypto, &tx.AmountCrypto, &tx.CallbackConfirmed, &tx.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (s *Store) PendingNotifications(ctx context.Context, maxRetries int) ([]Notification, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, txid, crypto, COALESCE(amount_crypto, 0), COALESCE(callback_confirmed, 0),
		type, COALESCE(retries, 0), object_id, callback_url, message, created_at
		FROM %s WHERE callback_confirmed = 0 AND retries < ? ORDER BY id LIMIT 200`, s.table("notification")), maxRetries)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.TxID, &n.Crypto, &n.AmountCrypto, &n.CallbackConfirmed, &n.Type, &n.Retries, &n.ObjectID, &n.CallbackURL, &n.Message, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) PayoutByID(ctx context.Context, id int64) (Payout, error) {
	var p Payout
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT id, created_at, updated_at, COALESCE(amount, 0), COALESCE(crypto, ''), COALESCE(dest_addr, ''),
		success, error, callback_url, task_id, external_id, COALESCE(status, 'IN_PROGRESS')
		FROM %s WHERE id = ? LIMIT 1`, s.table("payout")), id).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.Amount, &p.Crypto, &p.DestAddr, &p.Success, &p.Error, &p.CallbackURL, &p.TaskID, &p.ExternalID, &p.Status)
	if err != nil {
		return p, err
	}
	p.Transactions, err = s.PayoutTxs(ctx, p.ID)
	return p, err
}

func (s *Store) MarkPayoutSuccess(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, success = 'Yes', updated_at = %s WHERE id = ?", s.table("payout"), s.nowExpr()), PayoutSuccess, id)
	if err != nil {
		return err
	}
	return s.refreshOrderIndexForPayoutID(ctx, id)
}

func (s *Store) MarkPayoutFail(ctx context.Context, id int64, message string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, success = 'No', error = ?, updated_at = %s WHERE id = ?", s.table("payout"), s.nowExpr()), PayoutFail, message, id)
	if err != nil {
		return err
	}
	return s.refreshOrderIndexForPayoutID(ctx, id)
}

func (s *Store) CompletePayoutSuccess(ctx context.Context, payout Payout, txid string, createNotification bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, success = 'Yes', updated_at = %s WHERE id = ?", s.table("payout"), s.nowExpr()), PayoutSuccess, payout.ID); err != nil {
		return err
	}
	callbackURL := nullStringValue(payout.CallbackURL)
	if createNotification && callbackURL != "" && strings.TrimSpace(txid) != "" {
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (txid, crypto, amount_crypto, callback_confirmed, type, retries, object_id, callback_url)
			VALUES (?, ?, ?, 0, 'Payout', 0, ?, ?)`, s.table("notification")), txid, payout.Crypto, payout.Amount, payout.ID, callbackURL)
		if err != nil && !isDuplicateSchemaError(err) && !strings.Contains(strings.ToLower(err.Error()), "unique") {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.refreshOrderIndexForPayoutID(ctx, payout.ID)
}

func (s *Store) SetPayoutTaskAndTxIDs(ctx context.Context, payoutID int64, taskID string, txids []string) error {
	details := make([]PayoutTx, 0, len(txids))
	for _, txid := range uniqueNonEmptyStrings(txids) {
		details = append(details, PayoutTx{TxID: txid, Status: PayoutInProgress, Kind: "payout"})
	}
	return s.SetPayoutTaskAndTxDetails(ctx, payoutID, taskID, details)
}

func (s *Store) SetPayoutTaskAndTxDetails(ctx context.Context, payoutID int64, taskID string, details []PayoutTx) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if taskID != "" {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET task_id = ?, updated_at = %s WHERE id = ?", s.table("payout"), s.nowExpr()), taskID, payoutID); err != nil {
			return err
		}
	}
	for _, detail := range normalizePayoutTxDetails(details) {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
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
			payoutID,
			nullOrText(detail.TxID),
			detail.Status,
			detail.Kind,
			nullOrText(detail.SourceAddr),
			nullOrText(detail.DestAddr),
			detail.Amount,
			nullOrText(detail.Crypto),
			nullOrText(detail.Error),
		); err != nil && !isDuplicateSchemaError(err) && !strings.Contains(strings.ToLower(err.Error()), "unique") {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.refreshOrderIndexForPayoutID(ctx, payoutID)
}

func (s *Store) MarkPayoutPartial(ctx context.Context, id int64, message string) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = ?, success = 'Partial', error = ?, updated_at = %s WHERE id = ?", s.table("payout"), s.nowExpr()), PayoutPartial, strings.TrimSpace(message), id)
	if err != nil {
		return err
	}
	return s.refreshOrderIndexForPayoutID(ctx, id)
}

func normalizePayoutTxDetails(details []PayoutTx) []PayoutTx {
	out := make([]PayoutTx, 0, len(details))
	for _, detail := range details {
		detail.TxID = strings.TrimSpace(detail.TxID)
		detail.Status = strings.ToUpper(strings.TrimSpace(detail.Status))
		if detail.Status == "" {
			detail.Status = PayoutInProgress
		}
		detail.Kind = strings.ToLower(strings.TrimSpace(detail.Kind))
		if detail.Kind == "" {
			detail.Kind = "payout"
		}
		detail.Crypto = strings.ToUpper(strings.TrimSpace(detail.Crypto))
		if detail.TxID == "" && strings.TrimSpace(detail.Error) == "" {
			continue
		}
		out = append(out, detail)
	}
	return out
}

func (s *Store) PendingPayouts(ctx context.Context) ([]Payout, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id, created_at, updated_at, COALESCE(amount, 0), COALESCE(crypto, ''), COALESCE(dest_addr, ''),
		success, error, callback_url, task_id, external_id, COALESCE(status, 'IN_PROGRESS')
		FROM %s WHERE task_id IS NOT NULL AND status = ? AND created_at >= ? ORDER BY id LIMIT 200`, s.table("payout")), PayoutInProgress, timeNowMinusDays(1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Payout
	for rows.Next() {
		var p Payout
		if err := rows.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.Amount, &p.Crypto, &p.DestAddr, &p.Success, &p.Error, &p.CallbackURL, &p.TaskID, &p.ExternalID, &p.Status); err != nil {
			return nil, err
		}
		p.Transactions, err = s.PayoutTxs(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func timeNowMinusDays(days int) any {
	return time.Now().UTC().AddDate(0, 0, -days)
}
