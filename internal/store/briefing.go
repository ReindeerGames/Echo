package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/briefing"
	"github.com/ReindeerGames/echo/internal/domain"
)

func (s *Store) GetBriefingSettings(ctx context.Context) (domain.BriefingSettings, error) {
	var settings domain.BriefingSettings
	var lastDate sql.NullString
	var lastSent sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT enabled,local_time,timezone,last_sent_local_date,last_sent_at,updated_at
      FROM echo_briefing_settings WHERE singleton_id=1`).Scan(
		&settings.Enabled, &settings.LocalTime, &settings.Timezone, &lastDate, &lastSent, &settings.UpdatedAt)
	if err != nil {
		return domain.BriefingSettings{}, notFound(err)
	}
	if lastDate.Valid {
		settings.LastSentLocalDate = lastDate.String
	}
	if lastSent.Valid {
		value := lastSent.Time
		settings.LastSentAt = &value
	}
	return settings, nil
}

func (s *Store) SaveBriefingSettings(ctx context.Context, settings domain.BriefingSettings) (domain.BriefingSettings, error) {
	settings.LocalTime = strings.TrimSpace(settings.LocalTime)
	settings.Timezone = strings.TrimSpace(settings.Timezone)
	if err := briefing.Validate(settings); err != nil {
		return domain.BriefingSettings{}, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO echo_briefing_settings(singleton_id,enabled,local_time,timezone) VALUES(1,?,?,?)
      ON DUPLICATE KEY UPDATE enabled=VALUES(enabled),local_time=VALUES(local_time),timezone=VALUES(timezone)`, settings.Enabled, settings.LocalTime, settings.Timezone)
	if err != nil {
		return domain.BriefingSettings{}, err
	}
	return s.GetBriefingSettings(ctx)
}

func (s *Store) EnqueueDailyBriefIfDue(ctx context.Context, ownerJID, body string, now time.Time) (bool, error) {
	ownerJID = strings.TrimSpace(ownerJID)
	body = strings.TrimSpace(body)
	if ownerJID == "" || body == "" {
		return false, errors.New("daily briefing owner and body are required")
	}
	if len(ownerJID) > 255 {
		return false, errors.New("owner JID exceeds storage limits")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var settings domain.BriefingSettings
	var lastDate sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT enabled,local_time,timezone,last_sent_local_date
      FROM echo_briefing_settings WHERE singleton_id=1 FOR UPDATE`).Scan(
		&settings.Enabled, &settings.LocalTime, &settings.Timezone, &lastDate)
	if err != nil {
		return false, notFound(err)
	}
	if lastDate.Valid {
		settings.LastSentLocalDate = lastDate.String
	}
	due, err := briefing.Due(settings, now)
	if err != nil {
		return false, err
	}
	if !due {
		return false, nil
	}
	location, _ := time.LoadLocation(settings.Timezone)
	localDate := now.In(location).Format("2006-01-02")
	queued, err := enqueueProactiveOutbound(ctx, tx, ownerJID, body)
	if err != nil {
		return false, err
	}
	if !queued {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_briefing_settings SET last_sent_local_date=?,last_sent_at=? WHERE singleton_id=1`, localDate, now.UTC()); err != nil {
		return false, err
	}
	if err := insertAudit(ctx, tx, "system", "daily-brief-scheduler", "briefing.queued", "setting", "daily_brief", "succeeded", map[string]any{"localDate": localDate}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
