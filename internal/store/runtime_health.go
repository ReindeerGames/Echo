package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

func (s *Store) ReconcileRuntimeComponent(ctx context.Context, component, status string, healthy bool, now time.Time, ownerJID string) (*domain.RuntimeComponentTransition, error) {
	component = strings.TrimSpace(component)
	status = strings.TrimSpace(status)
	ownerJID = strings.TrimSpace(ownerJID)
	if !validRuntimeComponent(component) {
		return nil, errors.New("invalid runtime component name")
	}
	if healthy {
		if status != "running" && status != "disabled" {
			return nil, errors.New("invalid healthy runtime component status")
		}
	} else if status != "error" && status != "stopped" && status != "starting" {
		return nil, errors.New("invalid runtime component status")
	}
	if len(ownerJID) > 255 {
		return nil, errors.New("owner JID exceeds storage limits")
	}
	now = now.UTC()
	summary := runtimeComponentSummary(component, status, healthy)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var previous string
	err = tx.QueryRowContext(ctx, `SELECT alert_status FROM echo_runtime_component_alerts WHERE component_name=? FOR UPDATE`, component).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	exists := err == nil
	wasOpen := exists && previous == "open"
	err = nil
	var transition *domain.RuntimeComponentTransition

	switch {
	case !healthy && !wasOpen:
		kind := "opened"
		if exists {
			kind = "reopened"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO echo_runtime_component_alerts(component_name,alert_status,component_status,summary,opened_at,last_seen_at,resolved_at)
          VALUES(?,'open',?,?,?,?,NULL)
          ON DUPLICATE KEY UPDATE alert_status='open',component_status=VALUES(component_status),summary=VALUES(summary),opened_at=VALUES(opened_at),last_seen_at=VALUES(last_seen_at),resolved_at=NULL`, component, status, summary, now, now)
		transition = &domain.RuntimeComponentTransition{Kind: kind, Component: component, Status: status, Summary: summary}
	case !healthy && wasOpen:
		_, err = tx.ExecContext(ctx, `UPDATE echo_runtime_component_alerts SET component_status=?,summary=?,last_seen_at=? WHERE component_name=?`, status, summary, now, component)
	case healthy && wasOpen:
		_, err = tx.ExecContext(ctx, `UPDATE echo_runtime_component_alerts SET alert_status='resolved',component_status=?,summary=?,last_seen_at=?,resolved_at=? WHERE component_name=?`, status, summary, now, now, component)
		transition = &domain.RuntimeComponentTransition{Kind: "resolved", Component: component, Status: status, Summary: summary}
	}
	if err != nil {
		return nil, err
	}
	if transition != nil {
		if ownerJID != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO echo_whatsapp_outbox(id,chat_jid,body) VALUES(?,?,?)`, id.New("out"), ownerJID, runtimeComponentNotification(*transition)); err != nil {
				return nil, err
			}
		}
		if err := insertAudit(ctx, tx, "system", "echo-supervisor", "runtime.component_"+transition.Kind, "runtime_component", component, "succeeded", map[string]any{"status": transition.Status}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return transition, nil
}

func validRuntimeComponent(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, r := range value {
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-') {
			return false
		}
	}
	return true
}

func runtimeComponentSummary(component, status string, healthy bool) string {
	if healthy {
		if status == "disabled" {
			return fmt.Sprintf("%s is disabled by configuration; its previous alert was cleared", component)
		}
		return fmt.Sprintf("%s has remained running after automatic recovery", component)
	}
	return fmt.Sprintf("%s is %s; automatic restart is in progress", component, status)
}

func runtimeComponentNotification(transition domain.RuntimeComponentTransition) string {
	if transition.Kind == "resolved" {
		return "Echo runtime alert RESOLVED · " + transition.Component + "\n" + transition.Summary + ".\n\nNo action is required unless the component degrades again."
	}
	return "Echo runtime alert " + strings.ToUpper(transition.Kind) + " · " + transition.Component + "\n" + transition.Summary + ".\n\nOpen Echo → System → Runtime components to review the current state."
}
