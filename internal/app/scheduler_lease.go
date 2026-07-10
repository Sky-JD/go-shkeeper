package app

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (s *Store) TryAcquireSchedulerLease(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	name = strings.TrimSpace(name)
	owner = strings.TrimSpace(owner)
	if name == "" || owner == "" {
		return false, fmt.Errorf("scheduler lease name and owner are required")
	}
	if ttl <= 0 {
		ttl = 20 * time.Second
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(ttl)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `INSERT IGNORE INTO scheduler_lease (name, owner, lease_until) VALUES (?, ?, ?)`, name, owner, leaseUntil)
	if err != nil {
		return false, err
	}
	if rows, err := res.RowsAffected(); err != nil {
		return false, err
	} else if rows > 0 {
		return true, tx.Commit()
	}

	res, err = tx.ExecContext(ctx, `UPDATE scheduler_lease
		SET owner = ?, lease_until = ?, updated_at = CURRENT_TIMESTAMP(6)
		WHERE name = ? AND (owner = ? OR lease_until <= ?)`, owner, leaseUntil, name, owner, now)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (s *Store) ReleaseSchedulerLease(ctx context.Context, name, owner string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM scheduler_lease WHERE name = ? AND owner = ?`, strings.TrimSpace(name), strings.TrimSpace(owner))
	return err
}
