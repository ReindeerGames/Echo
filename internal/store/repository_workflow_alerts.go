package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

var ErrInvalidRepositoryWorkflowObservation = errors.New("invalid repository workflow observation")

func (s *Store) ListRepositoryWorkflowAlerts(ctx context.Context, status string) ([]domain.RepositoryWorkflowAlert, error) {
	status = strings.TrimSpace(status)
	query := repositoryWorkflowAlertSelect
	args := []any{}
	if status != "" {
		if status != "open" && status != "resolved" {
			return nil, ErrInvalidRepositoryWorkflowObservation
		}
		query += ` WHERE a.alert_status=?`
		args = append(args, status)
	}
	query += ` ORDER BY a.last_seen_at DESC,a.id LIMIT 500`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.RepositoryWorkflowAlert, 0)
	for rows.Next() {
		var item domain.RepositoryWorkflowAlert
		if err := scanRepositoryWorkflowAlert(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ReconcileRepositoryWorkflowAlert persists one repository-level failure
// lifecycle. Running or unknown observations never clear a previous failure;
// only fresh healthy evidence (or an explicitly disabled policy) can do that.
func (s *Store) ReconcileRepositoryWorkflowAlert(ctx context.Context, repositoryID, state, headSHA, summary string, failedWorkflows []string, now time.Time, ownerJID string) (*domain.RepositoryWorkflowTransition, error) {
	repositoryID = strings.TrimSpace(repositoryID)
	state = strings.TrimSpace(state)
	headSHA = strings.TrimSpace(headSHA)
	summary = strings.Join(strings.Fields(summary), " ")
	ownerJID = strings.TrimSpace(ownerJID)
	failedWorkflows = normalizedWorkflowNames(failedWorkflows)
	if len(repositoryID) != 29 || !stringInStore(state, "attention", "healthy", "running", "unknown", "disabled") || utf8.RuneCountInString(summary) > 300 || len(ownerJID) > 255 {
		return nil, ErrInvalidRepositoryWorkflowObservation
	}
	if state != "disabled" && !validRepositoryHeadSHA(headSHA) {
		return nil, ErrInvalidRepositoryWorkflowObservation
	}
	if state == "attention" && (len(failedWorkflows) == 0 || summary == "") {
		return nil, ErrInvalidRepositoryWorkflowObservation
	}
	namesJSON, err := json.Marshal(failedWorkflows)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var repository domain.Repository
	var projectName string
	if err := tx.QueryRowContext(ctx, `SELECT r.id,r.project_id,p.name,r.owner_name,r.repository_name,r.default_branch,r.enabled FROM echo_repositories r JOIN echo_projects p ON p.id=r.project_id WHERE r.id=? FOR UPDATE`, repositoryID).Scan(&repository.ID, &repository.ProjectID, &projectName, &repository.Owner, &repository.Name, &repository.DefaultBranch, &repository.Enabled); err != nil {
		return nil, notFound(err)
	}
	if !repository.Enabled && state != "disabled" {
		state = "disabled"
	}
	var existingID, previousStatus string
	err = tx.QueryRowContext(ctx, `SELECT id,alert_status FROM echo_repository_workflow_alerts WHERE repository_id=? FOR UPDATE`, repositoryID).Scan(&existingID, &previousStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	exists := err == nil
	wasOpen := exists && previousStatus == "open"
	err = nil
	var transition *domain.RepositoryWorkflowTransition

	switch {
	case state == "attention" && !wasOpen:
		kind := "opened"
		if exists {
			kind = "reopened"
		} else {
			existingID = id.New("rwa")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO echo_repository_workflow_alerts(id,repository_id,alert_status,summary,head_sha,failed_workflows_json,opened_at,last_seen_at,resolved_at)
          VALUES(?,?,'open',?,?,?,?,?,NULL)
          ON DUPLICATE KEY UPDATE alert_status='open',summary=VALUES(summary),head_sha=VALUES(head_sha),failed_workflows_json=VALUES(failed_workflows_json),opened_at=VALUES(opened_at),last_seen_at=VALUES(last_seen_at),resolved_at=NULL`, existingID, repositoryID, summary, headSHA, namesJSON, now, now)
		transition = &domain.RepositoryWorkflowTransition{Kind: kind}
	case state == "attention" && wasOpen:
		_, err = tx.ExecContext(ctx, `UPDATE echo_repository_workflow_alerts SET summary=?,head_sha=?,failed_workflows_json=?,last_seen_at=? WHERE repository_id=?`, summary, headSHA, namesJSON, now, repositoryID)
	case (state == "healthy" || state == "disabled") && wasOpen:
		resolution := "All latest default-branch workflows are healthy"
		if state == "disabled" {
			resolution = "Repository workflow monitoring was disabled"
		}
		_, err = tx.ExecContext(ctx, `UPDATE echo_repository_workflow_alerts SET alert_status='resolved',summary=?,head_sha=?,failed_workflows_json='[]',last_seen_at=?,resolved_at=? WHERE repository_id=?`, resolution, headSHA, now, now, repositoryID)
		transition = &domain.RepositoryWorkflowTransition{Kind: "resolved"}
	}
	if err != nil {
		return nil, err
	}
	if transition != nil {
		if err := scanRepositoryWorkflowAlert(tx.QueryRowContext(ctx, repositoryWorkflowAlertSelect+` WHERE a.repository_id=?`, repositoryID), &transition.Alert); err != nil {
			return nil, err
		}
		if ownerJID != "" {
			if _, err := enqueueProactiveOutbound(ctx, tx, ownerJID, repositoryWorkflowNotification(*transition)); err != nil {
				return nil, err
			}
		}
		if err := insertAudit(ctx, tx, "system", "github-watchdog", "github.workflow_alert_"+transition.Kind, "repository_workflow_alert", transition.Alert.ID, "succeeded", map[string]any{"repositoryId": repository.ID, "repository": repository.Owner + "/" + repository.Name, "headSha": headSHA, "failedWorkflowCount": len(failedWorkflows)}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return transition, nil
}

func normalizedWorkflowNames(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, min(len(values), 20))
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" || utf8.RuneCountInString(value) > 120 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) == 20 {
			break
		}
	}
	return out
}

func repositoryWorkflowNotification(transition domain.RepositoryWorkflowTransition) string {
	alert := transition.Alert
	if transition.Kind == "resolved" {
		return fmt.Sprintf("Echo GitHub workflow alert RESOLVED · %s\n%s.\n\nReply “repo %s” for the current default-branch evidence.", alert.Repository, alert.Summary, alert.Repository)
	}
	failed := strings.Join(alert.FailedWorkflows, ", ")
	return fmt.Sprintf("Echo GitHub workflow alert %s · %s\n%s.\nFailed: %s\n\nReply “repo %s” for current workflow evidence, or “github status” for the estate view.", strings.ToUpper(transition.Kind), alert.Repository, alert.Summary, failed, alert.Repository)
}

func stringInStore(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validRepositoryHeadSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

const repositoryWorkflowAlertSelect = `SELECT a.id,r.project_id,p.name,a.repository_id,CONCAT(r.owner_name,'/',r.repository_name),r.default_branch,a.alert_status,a.summary,a.head_sha,a.failed_workflows_json,a.opened_at,a.last_seen_at,a.resolved_at
  FROM echo_repository_workflow_alerts a
  JOIN echo_repositories r ON r.id=a.repository_id
  JOIN echo_projects p ON p.id=r.project_id`

func scanRepositoryWorkflowAlert(row scanner, item *domain.RepositoryWorkflowAlert) error {
	var failedJSON []byte
	var resolved sql.NullTime
	if err := row.Scan(&item.ID, &item.ProjectID, &item.ProjectName, &item.RepositoryID, &item.Repository, &item.DefaultBranch, &item.Status, &item.Summary, &item.HeadSHA, &failedJSON, &item.OpenedAt, &item.LastSeenAt, &resolved); err != nil {
		return err
	}
	if err := json.Unmarshal(failedJSON, &item.FailedWorkflows); err != nil {
		return err
	}
	if item.FailedWorkflows == nil {
		item.FailedWorkflows = []string{}
	}
	if resolved.Valid {
		value := resolved.Time.UTC()
		item.ResolvedAt = &value
	}
	return nil
}
