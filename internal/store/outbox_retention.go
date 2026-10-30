package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const sentOutboxPruneBatchSize = 500

// PruneSentOutbound removes only confirmed-sent delivery bodies in bounded
// batches. Unconfirmed and dead rows remain available for recovery or review.
func (s *Store) PruneSentOutbound(ctx context.Context, before time.Time) (int64, error) {
	before = before.UTC()
	var total int64
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM echo_whatsapp_outbox
          WHERE status='sent' AND sent_at<?
          ORDER BY sent_at,id
          LIMIT ?`, before, sentOutboxPruneBatchSize)
		if err != nil {
			return total, err
		}
		ids := make([]string, 0, sentOutboxPruneBatchSize)
		for rows.Next() {
			var outboxID string
			if err := rows.Scan(&outboxID); err != nil {
				_ = rows.Close()
				return total, err
			}
			ids = append(ids, outboxID)
		}
		if err := rows.Close(); err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		arguments := make([]any, 0, len(ids)+1)
		for _, outboxID := range ids {
			arguments = append(arguments, outboxID)
		}
		arguments = append(arguments, before)
		result, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM echo_whatsapp_outbox WHERE id IN (%s) AND status='sent' AND sent_at<?`, placeholders), arguments...)
		if err != nil {
			return total, err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += deleted
		if len(ids) < sentOutboxPruneBatchSize || deleted == 0 {
			return total, nil
		}
	}
}
