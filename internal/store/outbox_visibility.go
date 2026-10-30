package store

import (
	"context"
	"strings"

	"github.com/ReindeerGames/echo/internal/domain"
)

const outboundDeliveryMaxAttempts = 5

// ListOutboundDeliveries exposes queue mechanics and the safe coalesced item
// count without returning message bodies, destination identifiers, WhatsApp
// message IDs, coalescing keys, or raw errors.
func (s *Store) ListOutboundDeliveries(ctx context.Context, status string, limit int) ([]domain.OutboundDelivery, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	status = strings.ToLower(strings.TrimSpace(status))
	query := `SELECT CASE WHEN chat_jid LIKE '%@g.us' THEN 'group' ELSE 'direct' END,
	  status,coalesce_count,attempts,available_at,locked_until,last_error,created_at,sent_at
      FROM echo_whatsapp_outbox`
	args := []any{}
	switch status {
	case "queued", "sending", "failed", "dead", "sent":
		query += ` WHERE status=?`
		args = append(args, status)
	default:
		query += ` WHERE status IN ('queued','sending','failed','dead')`
	}
	query += ` ORDER BY FIELD(status,'dead','failed','sending','queued','sent'),created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.OutboundDelivery, 0, limit)
	for rows.Next() {
		var item domain.OutboundDelivery
		var rawError string
		if err := rows.Scan(&item.ChannelType, &item.Status, &item.BatchCount, &item.Attempts, &item.AvailableAt, &item.LockedUntil, &rawError, &item.CreatedAt, &item.SentAt); err != nil {
			return nil, err
		}
		item.MaxAttempts = outboundDeliveryMaxAttempts
		if rawError != "" {
			item.FailureCategory = domain.OutboundDeliveryErrorCategory(rawError)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
