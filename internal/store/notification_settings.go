package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

const maximumNotificationPause = 7 * 24 * time.Hour

func (s *Store) GetNotificationSettings(ctx context.Context) (domain.NotificationSettings, error) {
	var settings domain.NotificationSettings
	var pausedUntil sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT paused_until,COALESCE(paused_until>UTC_TIMESTAMP(6),FALSE),updated_at
      FROM echo_notification_settings WHERE singleton_id=1`).Scan(&pausedUntil, &settings.Paused, &settings.UpdatedAt)
	if err != nil {
		return domain.NotificationSettings{}, notFound(err)
	}
	if pausedUntil.Valid {
		value := pausedUntil.Time.UTC()
		settings.PausedUntil = &value
	}
	return settings, nil
}

// PauseProactiveNotifications atomically arms the bounded pause and suppresses
// proactive deliveries that have not entered an in-flight send attempt.
func (s *Store) PauseProactiveNotifications(ctx context.Context, until time.Time, actor string) (domain.NotificationSettings, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" || len(actor) > 255 {
		return domain.NotificationSettings{}, errors.New("notification pause requires a valid actor")
	}
	until = until.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NotificationSettings{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var now time.Time
	var current sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT UTC_TIMESTAMP(6),paused_until FROM echo_notification_settings WHERE singleton_id=1 FOR UPDATE`).Scan(&now, &current); err != nil {
		return domain.NotificationSettings{}, err
	}
	if !until.After(now) || until.After(now.Add(maximumNotificationPause)) {
		return domain.NotificationSettings{}, errors.New("notification pause must end within the next 7 days")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_notification_settings SET paused_until=?,updated_by=? WHERE singleton_id=1`, until, actor); err != nil {
		return domain.NotificationSettings{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE echo_whatsapp_outbox
      SET status='suppressed',suppressed_at=?,locked_until=NULL,coalesce_key=NULL,last_error=''
      WHERE delivery_class='proactive' AND status IN ('queued','failed')`, now)
	if err != nil {
		return domain.NotificationSettings{}, err
	}
	suppressed, err := result.RowsAffected()
	if err != nil {
		return domain.NotificationSettings{}, err
	}
	if err := insertAudit(ctx, tx, "admin", actor, "notifications.paused", "setting", "proactive_notifications", "succeeded", map[string]any{"pausedUntil": until, "suppressedPending": suppressed}); err != nil {
		return domain.NotificationSettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.NotificationSettings{}, err
	}
	settings, err := s.GetNotificationSettings(ctx)
	settings.SuppressedPending = suppressed
	return settings, err
}

func (s *Store) ResumeProactiveNotifications(ctx context.Context, actor string) (domain.NotificationSettings, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" || len(actor) > 255 {
		return domain.NotificationSettings{}, errors.New("notification resume requires a valid actor")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.NotificationSettings{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var current sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT paused_until FROM echo_notification_settings WHERE singleton_id=1 FOR UPDATE`).Scan(&current); err != nil {
		return domain.NotificationSettings{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_notification_settings SET paused_until=NULL,updated_by=? WHERE singleton_id=1`, actor); err != nil {
		return domain.NotificationSettings{}, err
	}
	if err := insertAudit(ctx, tx, "admin", actor, "notifications.resumed", "setting", "proactive_notifications", "succeeded", nil); err != nil {
		return domain.NotificationSettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.NotificationSettings{}, err
	}
	return s.GetNotificationSettings(ctx)
}

// proactiveNotificationsAllowed serializes proactive enqueue decisions with
// pause/resume updates. The caller must retain the transaction until its
// outbox mutation and related state transition have both committed.
func proactiveNotificationsAllowed(ctx context.Context, tx *sql.Tx) (bool, error) {
	var allowed bool
	if err := tx.QueryRowContext(ctx, `SELECT paused_until IS NULL OR paused_until<=UTC_TIMESTAMP(6)
      FROM echo_notification_settings WHERE singleton_id=1 FOR UPDATE`).Scan(&allowed); err != nil {
		return false, err
	}
	return allowed, nil
}

func enqueueProactiveOutbound(ctx context.Context, tx *sql.Tx, chatJID, body string) (bool, error) {
	allowed, err := proactiveNotificationsAllowed(ctx, tx)
	if err != nil || !allowed {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO echo_whatsapp_outbox(id,chat_jid,body,delivery_class) VALUES(?,?,?,'proactive')`, id.New("out"), chatJID, body); err != nil {
		return false, err
	}
	return true, nil
}
