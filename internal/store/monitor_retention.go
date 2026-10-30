package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const monitorPruneBatchSize = 500

// PruneMonitorEvents removes superseded monitoring history in small batches.
// It always preserves events referenced by an alert and the newest non-watchdog
// observation for each service so heartbeat freshness survives retention.
func (s *Store) PruneMonitorEvents(ctx context.Context, before time.Time) (int64, error) {
	before = before.UTC()
	var total int64
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT e.id
          FROM echo_monitor_events e
          LEFT JOIN echo_service_alerts a ON a.latest_event_id=e.event_id
          WHERE e.received_at<?
            AND a.id IS NULL
            AND (
              e.source_name='echo-watchdog'
              OR EXISTS (
                SELECT 1 FROM echo_monitor_events newer
                WHERE newer.service_id=e.service_id
                  AND newer.source_name<>'echo-watchdog'
                  AND (
                    newer.observed_at>e.observed_at
                    OR (newer.observed_at=e.observed_at AND newer.received_at>e.received_at)
                    OR (newer.observed_at=e.observed_at AND newer.received_at=e.received_at AND newer.id>e.id)
                  )
              )
            )
          ORDER BY e.received_at,e.id
          LIMIT ?`, before, monitorPruneBatchSize)
		if err != nil {
			return total, err
		}
		ids := make([]string, 0, monitorPruneBatchSize)
		for rows.Next() {
			var eventID string
			if err := rows.Scan(&eventID); err != nil {
				_ = rows.Close()
				return total, err
			}
			ids = append(ids, eventID)
		}
		if err := rows.Close(); err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		arguments := make([]any, len(ids))
		for index, eventID := range ids {
			arguments[index] = eventID
		}
		result, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM echo_monitor_events WHERE id IN (%s)`, placeholders), arguments...)
		if err != nil {
			return total, err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += deleted
		if len(ids) < monitorPruneBatchSize || deleted == 0 {
			return total, nil
		}
	}
}
