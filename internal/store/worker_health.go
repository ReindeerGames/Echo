package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

const workerQueueAlertKey = "queue-stalled"

func (s *Store) RecordWorkerHeartbeat(ctx context.Context, workerID string, observedAt time.Time) error {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 255 {
		return errors.New("worker heartbeat requires a valid worker ID")
	}
	observedAt = observedAt.UTC()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO echo_worker_heartbeats(worker_id,started_at,last_seen_at) VALUES(?,?,?)
      ON DUPLICATE KEY UPDATE last_seen_at=GREATEST(last_seen_at,VALUES(last_seen_at))`, workerID, observedAt, observedAt); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM echo_worker_heartbeats WHERE last_seen_at < DATE_SUB(?,INTERVAL 7 DAY)`, observedAt)
	return err
}

func (s *Store) ReconcileWorkerQueueHealth(ctx context.Context, now time.Time, heartbeatTimeout, queueStallTimeout time.Duration, workerExpected bool, ownerJID string) (domain.WorkerHealth, *domain.WorkerHealthTransition, error) {
	if heartbeatTimeout <= 0 || queueStallTimeout < heartbeatTimeout {
		return domain.WorkerHealth{}, nil, errors.New("invalid worker health timeouts")
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.WorkerHealth{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	health, err := readWorkerHealth(ctx, tx, now, heartbeatTimeout, queueStallTimeout, workerExpected)
	if err != nil {
		return domain.WorkerHealth{}, nil, err
	}
	var alertStatus string
	err = tx.QueryRowContext(ctx, `SELECT alert_status FROM echo_worker_health_alerts WHERE alert_key=? FOR UPDATE`, workerQueueAlertKey).Scan(&alertStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.WorkerHealth{}, nil, err
	}
	hadAlert := err == nil
	wasOpen := hadAlert && alertStatus == "open"
	var transition *domain.WorkerHealthTransition

	switch {
	case health.QueueStalled && !wasOpen:
		_, err = tx.ExecContext(ctx, `INSERT INTO echo_worker_health_alerts(alert_key,alert_status,ready_jobs,oldest_ready_at,worker_last_seen_at,summary,opened_at,last_seen_at,resolved_at)
          VALUES(?,'open',?,?,?,?,?,?,NULL)
          ON DUPLICATE KEY UPDATE alert_status='open',ready_jobs=VALUES(ready_jobs),oldest_ready_at=VALUES(oldest_ready_at),worker_last_seen_at=VALUES(worker_last_seen_at),summary=VALUES(summary),opened_at=VALUES(opened_at),last_seen_at=VALUES(last_seen_at),resolved_at=NULL`,
			workerQueueAlertKey, health.ReadyJobs, health.OldestReadyAt, health.LastSeenAt, health.Summary, now, now)
		if err != nil {
			return domain.WorkerHealth{}, nil, err
		}
		transition = &domain.WorkerHealthTransition{Kind: "opened", Health: health}
	case health.QueueStalled && wasOpen:
		_, err = tx.ExecContext(ctx, `UPDATE echo_worker_health_alerts SET ready_jobs=?,oldest_ready_at=?,worker_last_seen_at=?,summary=?,last_seen_at=? WHERE alert_key=?`, health.ReadyJobs, health.OldestReadyAt, health.LastSeenAt, health.Summary, now, workerQueueAlertKey)
		if err != nil {
			return domain.WorkerHealth{}, nil, err
		}
	case !health.QueueStalled && wasOpen:
		_, err = tx.ExecContext(ctx, `UPDATE echo_worker_health_alerts SET alert_status='resolved',ready_jobs=?,oldest_ready_at=?,worker_last_seen_at=?,summary=?,resolved_at=? WHERE alert_key=?`, health.ReadyJobs, health.OldestReadyAt, health.LastSeenAt, health.Summary, now, workerQueueAlertKey)
		if err != nil {
			return domain.WorkerHealth{}, nil, err
		}
		transition = &domain.WorkerHealthTransition{Kind: "resolved", Health: health}
	}

	if transition != nil && strings.TrimSpace(ownerJID) != "" {
		if len(strings.TrimSpace(ownerJID)) > 255 {
			return domain.WorkerHealth{}, nil, errors.New("owner JID exceeds storage limits")
		}
		body := workerHealthNotification(*transition)
		if _, err := tx.ExecContext(ctx, `INSERT INTO echo_whatsapp_outbox(id,chat_jid,body) VALUES(?,?,?)`, id.New("out"), strings.TrimSpace(ownerJID), body); err != nil {
			return domain.WorkerHealth{}, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.WorkerHealth{}, nil, err
	}
	return health, transition, nil
}

type workerHealthQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readWorkerHealth(ctx context.Context, query workerHealthQuery, now time.Time, heartbeatTimeout, queueStallTimeout time.Duration, workerExpected bool) (domain.WorkerHealth, error) {
	health := domain.WorkerHealth{Status: "unknown"}
	var lastSeen sql.NullTime
	err := query.QueryRowContext(ctx, `SELECT worker_id,last_seen_at FROM echo_worker_heartbeats ORDER BY last_seen_at DESC LIMIT 1`).Scan(&health.WorkerID, &lastSeen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return health, err
	}
	if lastSeen.Valid {
		seen := lastSeen.Time.UTC()
		health.LastSeenAt = &seen
		if seen.After(now.Add(-heartbeatTimeout)) {
			health.Status = "online"
		} else {
			health.Status = "stale"
		}
	}
	var oldest sql.NullTime
	if err := query.QueryRowContext(ctx, `SELECT COUNT(*),MIN(available_at) FROM echo_jobs WHERE status IN ('queued','failed') AND available_at<=? AND (locked_until IS NULL OR locked_until<?)`, now, now).Scan(&health.ReadyJobs, &oldest); err != nil {
		return health, err
	}
	if oldest.Valid {
		value := oldest.Time.UTC()
		health.OldestReadyAt = &value
	}
	if !workerExpected {
		health.Status = "disabled"
	}
	health.QueueStalled = workerExpected && health.ReadyJobs > 0 && health.OldestReadyAt != nil && !health.OldestReadyAt.After(now.Add(-queueStallTimeout)) && health.Status != "online"
	health.Summary = workerHealthSummary(health, now)
	return health, nil
}

func workerHealthSummary(health domain.WorkerHealth, now time.Time) string {
	if health.Status == "disabled" {
		return fmt.Sprintf("Worker execution disabled; %s", readyJobDescription(health.ReadyJobs))
	}
	if health.QueueStalled {
		return fmt.Sprintf("%s; oldest waiting %s; worker heartbeat %s", readyJobDescription(health.ReadyJobs), healthAge(now, health.OldestReadyAt), heartbeatDescription(now, health.LastSeenAt))
	}
	if health.Status == "online" {
		return fmt.Sprintf("Worker heartbeat fresh (%s ago); %s", healthAge(now, health.LastSeenAt), readyJobDescription(health.ReadyJobs))
	}
	if health.ReadyJobs == 0 {
		return fmt.Sprintf("No ready jobs; worker heartbeat %s", heartbeatDescription(now, health.LastSeenAt))
	}
	return fmt.Sprintf("%s within the stall grace period; worker heartbeat %s", readyJobDescription(health.ReadyJobs), heartbeatDescription(now, health.LastSeenAt))
}

func readyJobDescription(count int) string {
	if count == 1 {
		return "1 ready job"
	}
	return fmt.Sprintf("%d ready jobs", count)
}

func heartbeatDescription(now time.Time, value *time.Time) string {
	if value == nil {
		return "not recorded"
	}
	return "last seen " + healthAge(now, value) + " ago"
}

func healthAge(now time.Time, value *time.Time) string {
	if value == nil {
		return "unknown"
	}
	age := now.Sub(value.UTC())
	if age < 0 {
		age = 0
	}
	return age.Round(time.Second).String()
}

func workerHealthNotification(transition domain.WorkerHealthTransition) string {
	if transition.Kind == "resolved" {
		if transition.Health.Status == "disabled" {
			return "Echo worker queue alert cleared because worker execution is disabled.\n\n" + transition.Health.Summary + "\n\nQueued work will remain paused until the worker is enabled again."
		}
		return "Echo worker queue recovered.\n\n" + transition.Health.Summary + "\n\nNo action is required unless work starts accumulating again."
	}
	return "Echo worker queue is stalled.\n\n" + transition.Health.Summary + "\n\nOpen Echo → System → Job queue to inspect the waiting work. Echo will not retry dead mutation jobs blindly."
}
