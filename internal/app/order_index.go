package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type contextExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) rebuildOrderIndexIfEmpty(ctx context.Context) error {
	var count int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", s.table("order_index"))).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return s.RebuildOrderIndex(ctx)
}

func (s *Store) RebuildOrderIndex(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", s.table("order_index"))); err != nil {
		return err
	}
	for _, stmt := range []string{
		fmt.Sprintf(`INSERT INTO %s (external_id, sort_at)
			SELECT external_id, MAX(updated_at) FROM %s
			WHERE external_id IS NOT NULL AND external_id <> ''
			GROUP BY external_id
			ON DUPLICATE KEY UPDATE
				sort_at = GREATEST(sort_at, VALUES(sort_at)),
				updated_at = %s`, s.table("order_index"), s.table("invoice"), s.nowExpr()),
		fmt.Sprintf(`INSERT INTO %s (external_id, sort_at)
			SELECT external_id, MAX(updated_at) FROM %s
			WHERE external_id IS NOT NULL AND external_id <> ''
			GROUP BY external_id
			ON DUPLICATE KEY UPDATE
				sort_at = GREATEST(sort_at, VALUES(sort_at)),
				updated_at = %s`, s.table("order_index"), s.table("payout"), s.nowExpr()),
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) refreshOrderIndexForExternalID(ctx context.Context, externalID string) error {
	return s.refreshOrderIndexForExternalIDWith(ctx, s.db, externalID)
}

func (s *Store) refreshOrderIndexForExternalIDWith(ctx context.Context, db contextExecer, externalID string) error {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return nil
	}
	stmt := fmt.Sprintf(`INSERT INTO %s (external_id, sort_at)
		SELECT ?, MAX(sort_at) FROM (
			SELECT MAX(updated_at) AS sort_at FROM %s WHERE external_id = ?
			UNION ALL
			SELECT MAX(updated_at) AS sort_at FROM %s WHERE external_id = ?
		) sources
		HAVING MAX(sort_at) IS NOT NULL
		ON DUPLICATE KEY UPDATE
			sort_at = VALUES(sort_at),
			updated_at = %s`, s.table("order_index"), s.table("invoice"), s.table("payout"), s.nowExpr())
	_, err := db.ExecContext(ctx, stmt, externalID, externalID, externalID)
	return err
}

func (s *Store) refreshOrderIndexForInvoiceID(ctx context.Context, invoiceID int64) error {
	externalID, err := s.orderExternalIDByID(ctx, s.table("invoice"), invoiceID)
	if err != nil {
		return err
	}
	return s.refreshOrderIndexForExternalID(ctx, externalID)
}

func (s *Store) refreshOrderIndexForPayoutID(ctx context.Context, payoutID int64) error {
	externalID, err := s.orderExternalIDByID(ctx, s.table("payout"), payoutID)
	if err != nil {
		return err
	}
	return s.refreshOrderIndexForExternalID(ctx, externalID)
}

func (s *Store) orderExternalIDByID(ctx context.Context, table string, id int64) (string, error) {
	var externalID sql.NullString
	err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT external_id FROM %s WHERE id = ? LIMIT 1", table), id).Scan(&externalID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return nullStringValue(externalID), nil
}
