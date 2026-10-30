package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

var ErrMaintenanceOverlap = errors.New("maintenance window overlaps an existing window")

const (
	maintenanceWindowMinDuration = time.Minute
	maintenanceWindowMaxDuration = 30 * 24 * time.Hour
)

func (s *Store) CreateMaintenanceWindow(ctx context.Context, item domain.MaintenanceWindow, actor string) (domain.MaintenanceWindow, error) {
	item.ServiceID = strings.TrimSpace(item.ServiceID)
	item.Reason = strings.Join(strings.Fields(item.Reason), " ")
	actor = strings.TrimSpace(actor)
	item.StartsAt = item.StartsAt.UTC()
	item.EndsAt = item.EndsAt.UTC()
	duration := item.EndsAt.Sub(item.StartsAt)
	if item.ServiceID == "" || len(item.ServiceID) > 28 || len(item.Reason) < 3 || len(item.Reason) > 300 || actor == "" || len(actor) > 128 || duration < maintenanceWindowMinDuration || duration > maintenanceWindowMaxDuration {
		return domain.MaintenanceWindow{}, errors.New("invalid maintenance window")
	}
	item.ID = id.New("mnt")
	digest := sha256.Sum256([]byte(item.ServiceID))
	release, err := s.AcquireNamedLock(ctx, "echo-maintenance-"+hex.EncodeToString(digest[:16]), 5*time.Second)
	if err != nil {
		return domain.MaintenanceWindow{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MaintenanceWindow{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var overlap string
	err = tx.QueryRowContext(ctx, `SELECT id FROM echo_service_maintenance_windows
      WHERE service_id=? AND cancelled_at IS NULL AND starts_at<? AND ends_at>?
      ORDER BY starts_at LIMIT 1 FOR UPDATE`, item.ServiceID, item.EndsAt, item.StartsAt).Scan(&overlap)
	if err == nil {
		return domain.MaintenanceWindow{}, ErrMaintenanceOverlap
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.MaintenanceWindow{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO echo_service_maintenance_windows(id,service_id,starts_at,ends_at,reason,created_by) VALUES(?,?,?,?,?,?)`, item.ID, item.ServiceID, item.StartsAt, item.EndsAt, item.Reason, actor); err != nil {
		return domain.MaintenanceWindow{}, err
	}
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT UTC_TIMESTAMP(6)`).Scan(&now); err != nil {
		return domain.MaintenanceWindow{}, err
	}
	if !item.StartsAt.After(now) && item.EndsAt.After(now) {
		if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET maintenance_window_id=?,notification_suppressed_until=?,maintenance_opening_suppressed=FALSE WHERE service_id=? AND alert_status='open'`, item.ID, item.EndsAt, item.ServiceID); err != nil {
			return domain.MaintenanceWindow{}, err
		}
	}
	if err := scanMaintenanceWindow(tx.QueryRowContext(ctx, maintenanceWindowSelect+` WHERE m.id=?`, item.ID), &item); err != nil {
		return domain.MaintenanceWindow{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MaintenanceWindow{}, err
	}
	return item, nil
}

func (s *Store) ListMaintenanceWindows(ctx context.Context, serviceID string, limit int) ([]domain.MaintenanceWindow, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	query := maintenanceWindowSelect + ` WHERE m.cancelled_at IS NULL AND m.ends_at>UTC_TIMESTAMP(6)`
	args := []any{}
	if serviceID = strings.TrimSpace(serviceID); serviceID != "" {
		query += ` AND m.service_id=?`
		args = append(args, serviceID)
	}
	query += ` ORDER BY m.starts_at,m.ends_at LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.MaintenanceWindow, 0, limit)
	for rows.Next() {
		var item domain.MaintenanceWindow
		if err := scanMaintenanceWindow(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CancelMaintenanceWindow(ctx context.Context, windowID, ownerJID string) (domain.MaintenanceWindow, []domain.AlertTransition, error) {
	if len(windowID) != 28 {
		return domain.MaintenanceWindow{}, nil, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MaintenanceWindow{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var item domain.MaintenanceWindow
	if err := scanMaintenanceWindow(tx.QueryRowContext(ctx, maintenanceWindowSelect+` WHERE m.id=? AND m.cancelled_at IS NULL AND m.ends_at>UTC_TIMESTAMP(6) FOR UPDATE`, windowID), &item); err != nil {
		return domain.MaintenanceWindow{}, nil, notFound(err)
	}
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT UTC_TIMESTAMP(6)`).Scan(&now); err != nil {
		return domain.MaintenanceWindow{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_service_maintenance_windows SET cancelled_at=? WHERE id=?`, now, item.ID); err != nil {
		return domain.MaintenanceWindow{}, nil, err
	}
	item.CancelledAt = &now
	rows, err := tx.QueryContext(ctx, serviceAlertSelect+` WHERE a.maintenance_window_id=? AND a.alert_status='open' FOR UPDATE`, item.ID)
	if err != nil {
		return domain.MaintenanceWindow{}, nil, err
	}
	var transitions []domain.AlertTransition
	for rows.Next() {
		var alert domain.ServiceAlert
		if err := scanServiceAlert(rows, &alert); err != nil {
			_ = rows.Close()
			return domain.MaintenanceWindow{}, nil, err
		}
		alert.MaintenanceWindowID = ""
		alert.NotificationSuppressedUntil = nil
		alert.MaintenanceOpeningSuppressed = false
		transitions = append(transitions, domain.AlertTransition{Kind: "maintenance_ended", Alert: alert})
	}
	if err := rows.Close(); err != nil {
		return domain.MaintenanceWindow{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET maintenance_window_id=NULL,notification_suppressed_until=NULL,maintenance_opening_suppressed=FALSE WHERE maintenance_window_id=?`, item.ID); err != nil {
		return domain.MaintenanceWindow{}, nil, err
	}
	if err := enqueueMonitorTransitions(ctx, tx, ownerJID, transitions); err != nil {
		return domain.MaintenanceWindow{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MaintenanceWindow{}, nil, err
	}
	return item, transitions, nil
}

const maintenanceWindowSelect = `SELECT m.id,m.service_id,s.name,s.project_id,m.starts_at,m.ends_at,m.reason,m.cancelled_at,m.created_at
  FROM echo_service_maintenance_windows m JOIN echo_services s ON s.id=m.service_id`

func scanMaintenanceWindow(row scanner, item *domain.MaintenanceWindow) error {
	return row.Scan(&item.ID, &item.ServiceID, &item.ServiceName, &item.ProjectID, &item.StartsAt, &item.EndsAt, &item.Reason, &item.CancelledAt, &item.CreatedAt)
}

func activeMaintenanceWindow(ctx context.Context, tx *sql.Tx, serviceID string, now time.Time) (*domain.MaintenanceWindow, error) {
	var item domain.MaintenanceWindow
	err := scanMaintenanceWindow(tx.QueryRowContext(ctx, maintenanceWindowSelect+` WHERE m.service_id=? AND m.cancelled_at IS NULL AND m.starts_at<=? AND m.ends_at>? ORDER BY m.ends_at DESC LIMIT 1`, serviceID, now.UTC(), now.UTC()), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}
