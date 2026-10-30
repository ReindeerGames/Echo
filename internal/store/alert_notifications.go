package store

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

var ErrAlertTokenAmbiguous = errors.New("alert acknowledgement code is ambiguous")

const alertAcknowledgementCodeLength = 12

func AlertAcknowledgementCode(alertID string) string {
	alertID = strings.TrimSpace(alertID)
	if len(alertID) < alertAcknowledgementCodeLength {
		return ""
	}
	return strings.ToLower(alertID[len(alertID)-alertAcknowledgementCodeLength:])
}

func (s *Store) FindOpenServiceAlertByAcknowledgementCode(ctx context.Context, code string) (domain.ServiceAlert, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if !validAlertAcknowledgementCode(code) {
		return domain.ServiceAlert{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, serviceAlertSelect+` WHERE a.alert_status='open' AND RIGHT(a.id,?)=? ORDER BY a.opened_at DESC LIMIT 2`, alertAcknowledgementCodeLength, code)
	if err != nil {
		return domain.ServiceAlert{}, err
	}
	defer rows.Close()
	var matches []domain.ServiceAlert
	for rows.Next() {
		var alert domain.ServiceAlert
		if err := scanServiceAlert(rows, &alert); err != nil {
			return domain.ServiceAlert{}, err
		}
		matches = append(matches, alert)
	}
	if err := rows.Err(); err != nil {
		return domain.ServiceAlert{}, err
	}
	if len(matches) == 0 {
		return domain.ServiceAlert{}, ErrNotFound
	}
	if len(matches) > 1 {
		return domain.ServiceAlert{}, ErrAlertTokenAmbiguous
	}
	return matches[0], nil
}

func (s *Store) AcknowledgeServiceAlert(ctx context.Context, alertID, via string) (domain.ServiceAlert, bool, error) {
	via = strings.TrimSpace(via)
	if len(alertID) != 28 || (via != "control_plane" && via != "whatsapp") {
		return domain.ServiceAlert{}, false, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ServiceAlert{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var alert domain.ServiceAlert
	if err := scanServiceAlert(tx.QueryRowContext(ctx, serviceAlertSelect+` WHERE a.id=? AND a.alert_status='open' FOR UPDATE`, alertID), &alert); err != nil {
		return domain.ServiceAlert{}, false, notFound(err)
	}
	if alert.AcknowledgedAt != nil {
		if err := tx.Commit(); err != nil {
			return domain.ServiceAlert{}, false, err
		}
		return alert, false, nil
	}
	var acknowledgedAt time.Time
	if err := tx.QueryRowContext(ctx, `SELECT UTC_TIMESTAMP(6)`).Scan(&acknowledgedAt); err != nil {
		return domain.ServiceAlert{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET acknowledged_at=?,acknowledged_via=? WHERE id=?`, acknowledgedAt, via, alert.ID); err != nil {
		return domain.ServiceAlert{}, false, err
	}
	alert.AcknowledgedAt = &acknowledgedAt
	alert.AcknowledgedVia = via
	if err := tx.Commit(); err != nil {
		return domain.ServiceAlert{}, false, err
	}
	return alert, true, nil
}

func (s *Store) ClearServiceAlertAcknowledgement(ctx context.Context, alertID string) (domain.ServiceAlert, bool, error) {
	if len(alertID) != 28 {
		return domain.ServiceAlert{}, false, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ServiceAlert{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var alert domain.ServiceAlert
	if err := scanServiceAlert(tx.QueryRowContext(ctx, serviceAlertSelect+` WHERE a.id=? AND a.alert_status='open' FOR UPDATE`, alertID), &alert); err != nil {
		return domain.ServiceAlert{}, false, notFound(err)
	}
	if alert.AcknowledgedAt == nil {
		if err := tx.Commit(); err != nil {
			return domain.ServiceAlert{}, false, err
		}
		return alert, false, nil
	}
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT UTC_TIMESTAMP(6)`).Scan(&now); err != nil {
		return domain.ServiceAlert{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET acknowledged_at=NULL,acknowledged_via='',last_notified_at=? WHERE id=?`, now, alert.ID); err != nil {
		return domain.ServiceAlert{}, false, err
	}
	alert.AcknowledgedAt = nil
	alert.AcknowledgedVia = ""
	alert.LastNotifiedAt = &now
	if err := tx.Commit(); err != nil {
		return domain.ServiceAlert{}, false, err
	}
	return alert, true, nil
}

// QueueDueServiceAlertNotifications atomically reconciles expired maintenance
// suppression and queues bounded reminders for unacknowledged critical alerts.
// Row locking makes concurrent watchdog replicas safe without a process lock.
func (s *Store) QueueDueServiceAlertNotifications(ctx context.Context, now time.Time, reminderInterval time.Duration, ownerJID string, limit int) ([]domain.AlertTransition, error) {
	ownerJID = strings.TrimSpace(ownerJID)
	if ownerJID == "" {
		return []domain.AlertTransition{}, nil
	}
	if len(ownerJID) > 255 {
		return nil, errors.New("owner JID exceeds storage limits")
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	now = now.UTC()
	deadline := now.Add(-reminderInterval)
	remindersEnabled := reminderInterval > 0
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, serviceAlertSelect+`
      WHERE a.alert_status='open'
        AND NOT EXISTS (
          SELECT 1 FROM echo_service_maintenance_windows m
          WHERE m.service_id=a.service_id AND m.cancelled_at IS NULL AND m.starts_at<=? AND m.ends_at>?
        )
        AND (
          a.maintenance_window_id IS NOT NULL OR a.notification_suppressed_until IS NOT NULL OR
          (? AND a.severity='critical' AND a.acknowledged_at IS NULL AND COALESCE(a.last_notified_at,a.opened_at)<=?)
        )
      ORDER BY COALESCE(a.notification_suppressed_until,a.last_notified_at,a.opened_at),a.id
      LIMIT ? FOR UPDATE SKIP LOCKED`, now, now, remindersEnabled, deadline, limit)
	if err != nil {
		return nil, err
	}
	var alerts []domain.ServiceAlert
	for rows.Next() {
		var alert domain.ServiceAlert
		if err := scanServiceAlert(rows, &alert); err != nil {
			_ = rows.Close()
			return nil, err
		}
		alerts = append(alerts, alert)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	transitions := make([]domain.AlertTransition, 0, len(alerts))
	for _, alert := range alerts {
		expiredMaintenance := alert.MaintenanceWindowID != "" || alert.NotificationSuppressedUntil != nil
		if expiredMaintenance {
			if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET maintenance_window_id=NULL,notification_suppressed_until=NULL,maintenance_opening_suppressed=FALSE WHERE id=?`, alert.ID); err != nil {
				return nil, err
			}
			alert.MaintenanceWindowID = ""
			alert.NotificationSuppressedUntil = nil
			alert.MaintenanceOpeningSuppressed = false
		}
		kind := "reminder"
		if expiredMaintenance {
			kind = "maintenance_ended"
		}
		body := serviceAlertNotificationBody(kind, alert)
		if _, err := tx.ExecContext(ctx, `INSERT INTO echo_whatsapp_outbox(id,chat_jid,body) VALUES(?,?,?)`, id.New("out"), ownerJID, body); err != nil {
			return nil, err
		}
		if kind == "reminder" {
			alert.ReminderCount++
			if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET last_notified_at=?,reminder_count=reminder_count+1 WHERE id=?`, now, alert.ID); err != nil {
				return nil, err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET last_notified_at=? WHERE id=?`, now, alert.ID); err != nil {
			return nil, err
		}
		alert.LastNotifiedAt = &now
		transitions = append(transitions, domain.AlertTransition{Kind: kind, Alert: alert})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return transitions, nil
}

func validAlertAcknowledgementCode(code string) bool {
	if len(code) != alertAcknowledgementCodeLength {
		return false
	}
	_, err := hex.DecodeString(code)
	return err == nil
}

func serviceAlertNotificationBody(kind string, alert domain.ServiceAlert) string {
	label := strings.ReplaceAll(strings.ToUpper(kind), "_", " ")
	body := fmt.Sprintf("Echo alert %s · %s\n%s (%s)\n%s", label, strings.ToUpper(alert.Severity), alert.ServiceName, alert.Fingerprint, alert.Summary)
	if alert.Status == "open" {
		if alert.Severity == "critical" && alert.AcknowledgedAt == nil {
			body += fmt.Sprintf("\n\nReply “ack %s” to acknowledge this alert and stop reminders until it resolves, reopens, or escalates.", AlertAcknowledgementCode(alert.ID))
		}
		body += fmt.Sprintf("\nReply “service %s” for current evidence, or “investigate %s” for correlated diagnosis when AI tools are enabled.", alert.ServiceName, alert.ServiceName)
	}
	return body
}
