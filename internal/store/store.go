package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
	"github.com/go-sql-driver/mysql"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var ErrNotFound = errors.New("not found")
var ErrApprovalStale = errors.New("approval evidence revision is stale")

const (
	monitorNotificationMaxItems      = 10
	monitorNotificationMaxCharacters = 3500
	monitorNotificationOverflow      = "\n\n• Additional monitoring changes are available in Echo → Monitoring."
)

type Store struct {
	db *sql.DB
}

func (s *Store) SaveEncryptedSetting(ctx context.Context, key string, ciphertext []byte, metadata json.RawMessage) (time.Time, error) {
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO echo_encrypted_settings(setting_key,ciphertext,metadata_json) VALUES(?,?,?) ON DUPLICATE KEY UPDATE ciphertext=VALUES(ciphertext),metadata_json=VALUES(metadata_json)`, key, ciphertext, metadata)
	if err != nil {
		return time.Time{}, err
	}
	var updated time.Time
	err = s.db.QueryRowContext(ctx, `SELECT updated_at FROM echo_encrypted_settings WHERE setting_key=?`, key).Scan(&updated)
	return updated, err
}

func (s *Store) GetEncryptedSetting(ctx context.Context, key string) ([]byte, json.RawMessage, time.Time, error) {
	var ciphertext []byte
	var metadata json.RawMessage
	var updated time.Time
	err := s.db.QueryRowContext(ctx, `SELECT ciphertext,metadata_json,updated_at FROM echo_encrypted_settings WHERE setting_key=?`, key).Scan(&ciphertext, &metadata, &updated)
	return ciphertext, metadata, updated, notFound(err)
}

func (s *Store) DeleteEncryptedSetting(ctx context.Context, key string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM echo_encrypted_settings WHERE setting_key=?`, key)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AcquireNamedLock(ctx context.Context, name string, wait time.Duration) (func(), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var acquired int
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, ?)`, name, int(wait.Seconds())).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if acquired != 1 {
		_ = conn.Close()
		return nil, fmt.Errorf("named lock %q is held by another instance", name)
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT RELEASE_LOCK(?)`, name)
		_ = conn.Close()
	}, nil
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse core DSN: %w", err)
	}
	cfg.ParseTime = true
	cfg.Collation = "utf8mb4_bin"
	cfg.Timeout = defaultDuration(cfg.Timeout, 10*time.Second)
	cfg.ReadTimeout = defaultDuration(cfg.ReadTimeout, 30*time.Second)
	cfg.WriteTimeout = defaultDuration(cfg.WriteTimeout, 30*time.Second)
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open core database: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping core database: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) SchemaStatus(ctx context.Context) (domain.SchemaStatus, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return domain.SchemaStatus{}, err
	}
	expected := map[string]struct{}{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			expected[entry.Name()] = struct{}{}
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM echo_schema_migrations`)
	if err != nil {
		return domain.SchemaStatus{}, err
	}
	defer rows.Close()
	applied := 0
	unexpected := 0
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return domain.SchemaStatus{}, err
		}
		if _, ok := expected[version]; ok {
			applied++
		} else {
			unexpected++
		}
	}
	if err := rows.Err(); err != nil {
		return domain.SchemaStatus{}, err
	}
	status := domain.SchemaStatus{Expected: len(expected), Applied: applied, Missing: len(expected) - applied, Unexpected: unexpected}
	status.Current = status.Missing == 0 && status.Unexpected == 0
	return status, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `SELECT GET_LOCK('echo_core_migrations', 30)`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() { _, _ = s.db.ExecContext(context.Background(), `SELECT RELEASE_LOCK('echo_core_migrations')`) }()

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied int
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='echo_schema_migrations'`).Scan(&applied)
		if err != nil {
			return err
		}
		if applied > 0 {
			err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_schema_migrations WHERE version=?`, entry.Name()).Scan(&applied)
			if err != nil {
				return err
			}
			if applied > 0 {
				continue
			}
		}
		content, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		for _, statement := range strings.Split(string(content), "-- echo:split") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			if _, err := s.db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration %s: %w", entry.Name(), err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `INSERT IGNORE INTO echo_schema_migrations(version) VALUES (?)`, entry.Name()); err != nil {
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Store) Dashboard(ctx context.Context) (domain.Dashboard, error) {
	var out domain.Dashboard
	err := s.db.QueryRowContext(ctx, `SELECT
      (SELECT COUNT(*) FROM echo_incidents WHERE status NOT IN ('resolved','closed')),
      ((SELECT COUNT(*) FROM echo_service_alerts WHERE alert_status='open') +
       (SELECT COUNT(*) FROM echo_worker_health_alerts WHERE alert_status='open')),
      (SELECT COUNT(*) FROM echo_service_alerts a WHERE a.alert_status='open' AND EXISTS (
        SELECT 1 FROM echo_service_maintenance_windows m WHERE m.service_id=a.service_id AND m.cancelled_at IS NULL AND m.starts_at<=UTC_TIMESTAMP(6) AND m.ends_at>UTC_TIMESTAMP(6)
      )),
	  (SELECT COUNT(*) FROM echo_service_alerts a WHERE a.alert_status='open' AND a.acknowledged_at IS NOT NULL AND NOT EXISTS (
	    SELECT 1 FROM echo_service_maintenance_windows m WHERE m.service_id=a.service_id AND m.cancelled_at IS NULL AND m.starts_at<=UTC_TIMESTAMP(6) AND m.ends_at>UTC_TIMESTAMP(6)
	  )),
      (SELECT COUNT(*) FROM echo_service_maintenance_windows WHERE cancelled_at IS NULL AND starts_at<=UTC_TIMESTAMP(6) AND ends_at>UTC_TIMESTAMP(6)),
      (SELECT COUNT(*) FROM echo_approvals WHERE status='pending' AND expires_at > NOW(6)),
	  (SELECT COUNT(*) FROM echo_jobs WHERE status='queued'),
      (SELECT COUNT(*) FROM echo_jobs WHERE status='running'),
	  (SELECT COUNT(*) FROM echo_jobs WHERE status='failed'),
	  (SELECT COUNT(*) FROM echo_jobs WHERE status='dead'),
      (SELECT COUNT(*) FROM echo_deployments WHERE status='failed'),
	  (SELECT COUNT(*) FROM echo_projects WHERE enabled=TRUE),
	  (SELECT COUNT(*) FROM echo_whatsapp_outbox WHERE status IN ('queued','sending','failed')),
	  (SELECT COUNT(*) FROM echo_whatsapp_outbox WHERE status='dead'),
	  (SELECT MAX(last_seen_at) FROM echo_worker_heartbeats),
	  EXISTS(SELECT 1 FROM echo_worker_health_alerts WHERE alert_status='open'),
	  COALESCE((SELECT summary FROM echo_worker_health_alerts WHERE alert_status='open' ORDER BY updated_at DESC LIMIT 1),'')`).Scan(
		&out.OpenIncidents, &out.OpenAlerts, &out.SuppressedAlerts, &out.AcknowledgedAlerts, &out.ActiveMaintenanceWindows, &out.PendingApprovals, &out.QueuedJobs, &out.RunningJobs, &out.RetryingJobs, &out.DeadJobs, &out.FailedDeployments, &out.EnabledProjects, &out.PendingWhatsAppMessages, &out.DeadWhatsAppMessages, &out.WorkerLastSeenAt, &out.WorkerQueueStalled, &out.WorkerHealthSummary,
	)
	return out, err
}

func (s *Store) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, project_key, name, description, enabled, created_at, updated_at FROM echo_projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Project
	for rows.Next() {
		var item domain.Project
		if err := rows.Scan(&item.ID, &item.Key, &item.Name, &item.Description, &item.Enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SaveProject(ctx context.Context, item domain.Project) (domain.Project, error) {
	if item.ID == "" {
		item.ID = id.New("prj")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO echo_projects(id, project_key, name, description, enabled) VALUES(?,?,?,?,?)
      ON DUPLICATE KEY UPDATE project_key=VALUES(project_key), name=VALUES(name), description=VALUES(description), enabled=VALUES(enabled)`,
		item.ID, item.Key, item.Name, item.Description, item.Enabled)
	if err != nil {
		return domain.Project{}, err
	}
	return s.GetProject(ctx, item.ID)
}

func (s *Store) GetProject(ctx context.Context, projectID string) (domain.Project, error) {
	var item domain.Project
	err := s.db.QueryRowContext(ctx, `SELECT id, project_key, name, description, enabled, created_at, updated_at FROM echo_projects WHERE id=?`, projectID).Scan(
		&item.ID, &item.Key, &item.Name, &item.Description, &item.Enabled, &item.CreatedAt, &item.UpdatedAt)
	return item, notFound(err)
}

func (s *Store) ListGroupBindings(ctx context.Context) ([]domain.GroupBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id,g.project_id,p.name,g.group_jid,g.group_name,g.mention_required,g.ack_template,g.enabled,g.created_at,g.updated_at FROM echo_group_bindings g JOIN echo_projects p ON p.id=g.project_id ORDER BY g.group_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.GroupBinding
	for rows.Next() {
		var item domain.GroupBinding
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.ProjectName, &item.GroupJID, &item.GroupName, &item.MentionRequired, &item.AckTemplate, &item.Enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SaveGroupBinding(ctx context.Context, item domain.GroupBinding) (domain.GroupBinding, error) {
	if item.ID == "" {
		item.ID = id.New("grp")
	}
	if item.AckTemplate == "" {
		item.AckTemplate = "Thanks — I have logged this report and will investigate it privately with the owner."
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO echo_group_bindings(id,project_id,group_jid,group_name,mention_required,ack_template,enabled) VALUES(?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE project_id=VALUES(project_id),group_jid=VALUES(group_jid),group_name=VALUES(group_name),mention_required=VALUES(mention_required),ack_template=VALUES(ack_template),enabled=VALUES(enabled)`, item.ID, item.ProjectID, item.GroupJID, item.GroupName, item.MentionRequired, item.AckTemplate, item.Enabled)
	if err != nil {
		return domain.GroupBinding{}, err
	}
	return s.GetGroupBinding(ctx, item.ID)
}

func (s *Store) GetGroupBinding(ctx context.Context, bindingID string) (domain.GroupBinding, error) {
	var item domain.GroupBinding
	err := s.db.QueryRowContext(ctx, `SELECT g.id,g.project_id,p.name,g.group_jid,g.group_name,g.mention_required,g.ack_template,g.enabled,g.created_at,g.updated_at FROM echo_group_bindings g JOIN echo_projects p ON p.id=g.project_id WHERE g.id=?`, bindingID).Scan(&item.ID, &item.ProjectID, &item.ProjectName, &item.GroupJID, &item.GroupName, &item.MentionRequired, &item.AckTemplate, &item.Enabled, &item.CreatedAt, &item.UpdatedAt)
	return item, notFound(err)
}

func (s *Store) FindGroupBinding(ctx context.Context, groupJID string) (domain.GroupBinding, error) {
	var item domain.GroupBinding
	err := s.db.QueryRowContext(ctx, `SELECT g.id,g.project_id,p.name,g.group_jid,g.group_name,g.mention_required,g.ack_template,g.enabled,g.created_at,g.updated_at FROM echo_group_bindings g JOIN echo_projects p ON p.id=g.project_id WHERE g.group_jid=? AND g.enabled=TRUE AND p.enabled=TRUE`, groupJID).Scan(&item.ID, &item.ProjectID, &item.ProjectName, &item.GroupJID, &item.GroupName, &item.MentionRequired, &item.AckTemplate, &item.Enabled, &item.CreatedAt, &item.UpdatedAt)
	return item, notFound(err)
}

func (s *Store) ListRepositories(ctx context.Context, projectID string) ([]domain.Repository, error) {
	query := `SELECT id,project_id,owner_name,repository_name,repository_role,default_branch,read_only,enabled,test_command,created_at,updated_at FROM echo_repositories`
	args := []any{}
	if projectID != "" {
		query += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY owner_name, repository_name`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Repository
	for rows.Next() {
		var item domain.Repository
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Owner, &item.Name, &item.Role, &item.DefaultBranch, &item.ReadOnly, &item.Enabled, &item.TestCommand, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SaveRepository(ctx context.Context, item domain.Repository) (domain.Repository, error) {
	if item.DefaultBranch == "" {
		item.DefaultBranch = "main"
	}
	if item.Role == "" {
		item.Role = "application"
	}
	if item.ID == "" {
		item.ID = id.New("repo")
		if _, err := s.db.ExecContext(ctx, `INSERT INTO echo_repositories(id,project_id,owner_name,repository_name,repository_role,default_branch,read_only,enabled,test_command) VALUES(?,?,?,?,?,?,?,?,?)`, item.ID, item.ProjectID, item.Owner, item.Name, item.Role, item.DefaultBranch, item.ReadOnly, item.Enabled, item.TestCommand); err != nil {
			return domain.Repository{}, err
		}
		return s.GetRepository(ctx, item.ID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Repository{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var currentProjectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM echo_repositories WHERE id=? FOR UPDATE`, item.ID).Scan(&currentProjectID); err != nil {
		return domain.Repository{}, notFound(err)
	}
	if currentProjectID != item.ProjectID {
		var linkCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_service_repositories WHERE repository_id=?`, item.ID).Scan(&linkCount); err != nil {
			return domain.Repository{}, err
		}
		if linkCount > 0 {
			return domain.Repository{}, ErrRepositoryHasServiceLinks
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_repositories SET project_id=?,owner_name=?,repository_name=?,repository_role=?,default_branch=?,read_only=?,enabled=?,test_command=? WHERE id=?`, item.ProjectID, item.Owner, item.Name, item.Role, item.DefaultBranch, item.ReadOnly, item.Enabled, item.TestCommand, item.ID); err != nil {
		return domain.Repository{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Repository{}, err
	}
	return s.GetRepository(ctx, item.ID)
}

func (s *Store) GetRepository(ctx context.Context, repositoryID string) (domain.Repository, error) {
	var out domain.Repository
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,owner_name,repository_name,repository_role,default_branch,read_only,enabled,test_command,created_at,updated_at FROM echo_repositories WHERE id=?`, repositoryID).Scan(&out.ID, &out.ProjectID, &out.Owner, &out.Name, &out.Role, &out.DefaultBranch, &out.ReadOnly, &out.Enabled, &out.TestCommand, &out.CreatedAt, &out.UpdatedAt)
	return out, notFound(err)
}

func (s *Store) ListEnvironments(ctx context.Context, projectID string) ([]domain.Environment, error) {
	query := `SELECT id,project_id,name,kind,requires_approval,protected,config_json,created_at,updated_at FROM echo_environments`
	args := []any{}
	if projectID != "" {
		query += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY project_id,name`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Environment
	for rows.Next() {
		var item domain.Environment
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Name, &item.Kind, &item.RequiresApproval, &item.Protected, &item.Config, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SaveEnvironment(ctx context.Context, item domain.Environment) (domain.Environment, error) {
	if item.ID == "" {
		item.ID = id.New("env")
	}
	if item.Kind == "" {
		item.Kind = "github_actions"
	}
	if len(item.Config) == 0 {
		item.Config = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO echo_environments(id,project_id,name,kind,requires_approval,protected,config_json) VALUES(?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE project_id=VALUES(project_id),name=VALUES(name),kind=VALUES(kind),requires_approval=VALUES(requires_approval),protected=VALUES(protected),config_json=VALUES(config_json)`, item.ID, item.ProjectID, item.Name, item.Kind, item.RequiresApproval, item.Protected, item.Config)
	if err != nil {
		return domain.Environment{}, err
	}
	return s.GetEnvironment(ctx, item.ID)
}
func (s *Store) GetEnvironment(ctx context.Context, environmentID string) (domain.Environment, error) {
	var item domain.Environment
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,name,kind,requires_approval,protected,config_json,created_at,updated_at FROM echo_environments WHERE id=?`, environmentID).Scan(&item.ID, &item.ProjectID, &item.Name, &item.Kind, &item.RequiresApproval, &item.Protected, &item.Config, &item.CreatedAt, &item.UpdatedAt)
	return item, notFound(err)
}

func (s *Store) ListServices(ctx context.Context, projectID string) ([]domain.Service, error) {
	query := `SELECT s.id,s.project_id,p.name,s.service_key,s.name,s.service_kind,s.criticality,s.description,s.enabled,s.heartbeat_timeout_seconds,s.metadata_json,s.created_at,s.updated_at FROM echo_services s JOIN echo_projects p ON p.id=s.project_id`
	args := []any{}
	if projectID != "" {
		query += ` WHERE s.project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY p.name,s.name`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Service
	for rows.Next() {
		var item domain.Service
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.ProjectName, &item.Key, &item.Name, &item.Kind, &item.Criticality, &item.Description, &item.Enabled, &item.HeartbeatTimeoutSeconds, &item.Metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SaveService(ctx context.Context, item domain.Service) (domain.Service, error) {
	if item.Kind == "" {
		item.Kind = "application"
	}
	if item.Criticality == "" {
		item.Criticality = "medium"
	}
	if len(item.Metadata) == 0 {
		item.Metadata = json.RawMessage(`{}`)
	}
	var err error
	if item.ID == "" {
		item.ID = id.New("svc")
		_, err = s.db.ExecContext(ctx, `INSERT INTO echo_services(id,project_id,service_key,name,service_kind,criticality,description,enabled,heartbeat_timeout_seconds,metadata_json) VALUES(?,?,?,?,?,?,?,?,?,?)`, item.ID, item.ProjectID, item.Key, item.Name, item.Kind, item.Criticality, item.Description, item.Enabled, item.HeartbeatTimeoutSeconds, item.Metadata)
	} else {
		tx, beginErr := s.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return domain.Service{}, beginErr
		}
		defer func() { _ = tx.Rollback() }()
		var currentProjectID string
		if scanErr := tx.QueryRowContext(ctx, `SELECT project_id FROM echo_services WHERE id=? FOR UPDATE`, item.ID).Scan(&currentProjectID); scanErr != nil {
			return domain.Service{}, notFound(scanErr)
		}
		if currentProjectID != item.ProjectID {
			var dependencyCount int
			if countErr := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM echo_service_dependencies WHERE service_id=? OR depends_on_service_id=?) + (SELECT COUNT(*) FROM echo_service_repositories WHERE service_id=?)`, item.ID, item.ID, item.ID).Scan(&dependencyCount); countErr != nil {
				return domain.Service{}, countErr
			}
			if dependencyCount > 0 {
				return domain.Service{}, ErrServiceHasDependencies
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE echo_services SET project_id=?,service_key=?,name=?,service_kind=?,criticality=?,description=?,enabled=?,heartbeat_timeout_seconds=?,metadata_json=? WHERE id=?`, item.ProjectID, item.Key, item.Name, item.Kind, item.Criticality, item.Description, item.Enabled, item.HeartbeatTimeoutSeconds, item.Metadata, item.ID); err != nil {
			return domain.Service{}, err
		}
		if err = tx.Commit(); err != nil {
			return domain.Service{}, err
		}
	}
	if err != nil {
		return domain.Service{}, err
	}
	return s.GetService(ctx, item.ID)
}

func (s *Store) GetService(ctx context.Context, serviceID string) (domain.Service, error) {
	var item domain.Service
	err := s.db.QueryRowContext(ctx, `SELECT s.id,s.project_id,p.name,s.service_key,s.name,s.service_kind,s.criticality,s.description,s.enabled,s.heartbeat_timeout_seconds,s.metadata_json,s.created_at,s.updated_at FROM echo_services s JOIN echo_projects p ON p.id=s.project_id WHERE s.id=?`, serviceID).Scan(&item.ID, &item.ProjectID, &item.ProjectName, &item.Key, &item.Name, &item.Kind, &item.Criticality, &item.Description, &item.Enabled, &item.HeartbeatTimeoutSeconds, &item.Metadata, &item.CreatedAt, &item.UpdatedAt)
	return item, notFound(err)
}

func (s *Store) ListMonitorEvents(ctx context.Context, serviceID string, limit int) ([]domain.MonitorEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	query := `SELECT e.id,e.event_id,e.service_id,s.name,e.source_name,e.health_status,e.severity,e.summary,e.fingerprint,e.details_json,e.observed_at,e.received_at FROM echo_monitor_events e JOIN echo_services s ON s.id=e.service_id`
	args := []any{}
	if serviceID != "" {
		query += ` WHERE e.service_id=?`
		args = append(args, serviceID)
	}
	query += ` ORDER BY e.observed_at DESC,e.received_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MonitorEvent
	for rows.Next() {
		var item domain.MonitorEvent
		if err := scanMonitorEvent(rows, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListLatestMonitorEvents(ctx context.Context, projectID string) ([]domain.MonitorEvent, error) {
	query := `SELECT e.id,e.event_id,e.service_id,s.name,e.source_name,e.health_status,e.severity,e.summary,e.fingerprint,e.details_json,e.observed_at,e.received_at
	  FROM echo_monitor_events e
	  JOIN echo_services s ON s.id=e.service_id
	  WHERE e.id=(
	    SELECT e2.id FROM echo_monitor_events e2
	    WHERE e2.service_id=e.service_id AND e2.source_name<>'echo-watchdog'
	    ORDER BY e2.observed_at DESC,e2.received_at DESC LIMIT 1
	  )`
	args := []any{}
	if projectID != "" {
		query += ` AND s.project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY s.name,e.observed_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MonitorEvent
	for rows.Next() {
		var item domain.MonitorEvent
		if err := scanMonitorEvent(rows, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListServiceAlerts(ctx context.Context, status string) ([]domain.ServiceAlert, error) {
	query := serviceAlertSelect
	args := []any{}
	if status == "open" || status == "resolved" {
		query += ` WHERE a.alert_status=?`
		args = append(args, status)
	}
	query += ` ORDER BY FIELD(a.alert_status,'open','resolved'),FIELD(a.severity,'critical','warning'),a.last_seen_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ServiceAlert
	for rows.Next() {
		var item domain.ServiceAlert
		if err := scanServiceAlert(rows, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListServiceAlertsForService(ctx context.Context, serviceID, status string, limit int) ([]domain.ServiceAlert, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	query := serviceAlertSelect + ` WHERE a.service_id=?`
	args := []any{serviceID}
	if status == "open" || status == "resolved" {
		query += ` AND a.alert_status=?`
		args = append(args, status)
	}
	query += ` ORDER BY FIELD(a.alert_status,'open','resolved'),FIELD(a.severity,'critical','warning'),a.last_seen_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ServiceAlert, 0, limit)
	for rows.Next() {
		var item domain.ServiceAlert
		if err := scanServiceAlert(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListStaleServiceHeartbeats(ctx context.Context, now time.Time) ([]domain.StaleServiceHeartbeat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id,s.name,s.criticality,s.heartbeat_timeout_seconds,COALESCE(e.event_id,''),e.observed_at,s.updated_at,GREATEST(s.updated_at,COALESCE(e.observed_at,'1970-01-01 00:00:00')) AS policy_reference
      FROM echo_services s
      LEFT JOIN echo_monitor_events e ON e.id=(
        SELECT e2.id FROM echo_monitor_events e2
        WHERE e2.service_id=s.id AND e2.source_name<>'echo-watchdog'
        ORDER BY e2.observed_at DESC,e2.received_at DESC LIMIT 1
      )
      WHERE s.enabled=TRUE AND s.heartbeat_timeout_seconds>0
      HAVING TIMESTAMPADD(SECOND,heartbeat_timeout_seconds,policy_reference)<=?`, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.StaleServiceHeartbeat
	for rows.Next() {
		var item domain.StaleServiceHeartbeat
		var observed sql.NullTime
		if err := rows.Scan(&item.ServiceID, &item.ServiceName, &item.Criticality, &item.TimeoutSeconds, &item.LastEventID, &observed, &item.PolicyUpdatedAt, &item.PolicyReference); err != nil {
			return nil, err
		}
		if observed.Valid {
			value := observed.Time.UTC()
			item.LastObservedAt = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ResolveInactiveHeartbeatAlerts(ctx context.Context, now time.Time, ownerJID ...string) ([]domain.AlertTransition, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, serviceAlertSelect+`
      WHERE a.alert_status='open' AND a.fingerprint='observer-silent' AND (s.enabled=FALSE OR s.heartbeat_timeout_seconds=0)
      FOR UPDATE`)
	if err != nil {
		return nil, err
	}
	var transitions []domain.AlertTransition
	for rows.Next() {
		var alert domain.ServiceAlert
		if err := scanServiceAlert(rows, &alert); err != nil {
			_ = rows.Close()
			return nil, err
		}
		alert.Status = "resolved"
		alert.LastSeenAt = now.UTC()
		resolved := now.UTC()
		alert.ResolvedAt = &resolved
		alert.LatestEventID = "echo-watchdog:policy-disabled"
		transition := domain.AlertTransition{Kind: "resolved", Alert: alert}
		if alert.MaintenanceOpeningSuppressed {
			transition.Suppressed = true
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, transition := range transitions {
		alert := transition.Alert
		if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET alert_status='resolved',latest_event_id=?,last_seen_at=?,resolved_at=?,maintenance_window_id=NULL,notification_suppressed_until=NULL,maintenance_opening_suppressed=FALSE WHERE id=?`, alert.LatestEventID, alert.LastSeenAt, alert.ResolvedAt, alert.ID); err != nil {
			return nil, err
		}
	}
	if err := enqueueMonitorTransitions(ctx, tx, firstString(ownerJID), transitions); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return transitions, nil
}

func (s *Store) RecordMonitorEvent(ctx context.Context, event domain.MonitorEvent, ownerJID ...string) (domain.MonitorResult, error) {
	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := s.recordMonitorEvent(ctx, event, ownerJID...)
		if err == nil || !retryableTransactionError(err) {
			return result, err
		}
		lastErr = err
		if attempt == maxAttempts {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return domain.MonitorResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return domain.MonitorResult{}, lastErr
}

func retryableTransactionError(err error) bool {
	var databaseError *mysql.MySQLError
	return errors.As(err, &databaseError) && (databaseError.Number == 1205 || databaseError.Number == 1213)
}

func (s *Store) recordMonitorEvent(ctx context.Context, event domain.MonitorEvent, ownerJID ...string) (domain.MonitorResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MonitorResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if event.ID == "" {
		event.ID = id.New("evt")
	}
	result, err := tx.ExecContext(ctx, `INSERT IGNORE INTO echo_monitor_events(id,event_id,service_id,source_name,health_status,severity,summary,fingerprint,details_json,observed_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, event.ID, event.EventID, event.ServiceID, event.Source, event.Status, event.Severity, event.Summary, event.Fingerprint, event.Details, event.ObservedAt)
	if err != nil {
		return domain.MonitorResult{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.MonitorResult{}, err
	}
	if affected == 0 {
		var existing domain.MonitorEvent
		err = tx.QueryRowContext(ctx, `SELECT e.id,e.event_id,e.service_id,s.name,e.source_name,e.health_status,e.severity,e.summary,e.fingerprint,e.details_json,e.observed_at,e.received_at FROM echo_monitor_events e JOIN echo_services s ON s.id=e.service_id WHERE e.event_id=?`, event.EventID).Scan(monitorEventScanArgs(&existing)...)
		if err != nil {
			return domain.MonitorResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.MonitorResult{}, err
		}
		return domain.MonitorResult{Event: existing, Duplicate: true, Transitions: []domain.AlertTransition{}}, nil
	}

	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT UTC_TIMESTAMP(6)`).Scan(&now); err != nil {
		return domain.MonitorResult{}, err
	}
	if err := lockNotificationCoalesce(ctx, tx, "monitor-alert-lifecycle"); err != nil {
		return domain.MonitorResult{}, err
	}
	maintenance, err := activeMaintenanceWindow(ctx, tx, event.ServiceID, now)
	if err != nil {
		return domain.MonitorResult{}, err
	}
	transitions, err := updateAlerts(ctx, tx, event)
	if err != nil {
		return domain.MonitorResult{}, err
	}
	transitions, err = applyMaintenancePolicy(ctx, tx, transitions, maintenance)
	if err != nil {
		return domain.MonitorResult{}, err
	}
	if err := enqueueMonitorTransitions(ctx, tx, firstString(ownerJID), transitions); err != nil {
		return domain.MonitorResult{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT e.id,e.event_id,e.service_id,s.name,e.source_name,e.health_status,e.severity,e.summary,e.fingerprint,e.details_json,e.observed_at,e.received_at FROM echo_monitor_events e JOIN echo_services s ON s.id=e.service_id WHERE e.id=?`, event.ID).Scan(monitorEventScanArgs(&event)...); err != nil {
		return domain.MonitorResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MonitorResult{}, err
	}
	return domain.MonitorResult{Event: event, Transitions: transitions}, nil
}

func enqueueMonitorTransitions(ctx context.Context, tx *sql.Tx, ownerJID string, transitions []domain.AlertTransition) error {
	ownerJID = strings.TrimSpace(ownerJID)
	if ownerJID == "" {
		return nil
	}
	if len(ownerJID) > 255 {
		return errors.New("owner JID exceeds storage limits")
	}
	if err := enqueueMonitorTransitionBodies(ctx, tx, ownerJID, transitions); err != nil {
		return err
	}
	for _, transition := range transitions {
		if transition.Suppressed || !monitorTransitionNotifiable(transition.Kind) {
			continue
		}
		alert := transition.Alert
		if alert.Status == "open" {
			if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET last_notified_at=UTC_TIMESTAMP(6) WHERE id=?`, alert.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func enqueueMonitorTransitionBodies(ctx context.Context, tx *sql.Tx, ownerJID string, transitions []domain.AlertTransition) error {
	if ownerJID == "" {
		return nil
	}
	kinds := make(map[string]struct{})
	for _, transition := range transitions {
		if !transition.Suppressed && monitorTransitionNotifiable(transition.Kind) {
			kinds[transition.Kind] = struct{}{}
		}
	}
	orderedKinds := make([]string, 0, len(kinds))
	for kind := range kinds {
		orderedKinds = append(orderedKinds, kind)
	}
	sort.Strings(orderedKinds)
	for _, kind := range orderedKinds {
		if err := lockNotificationCoalesce(ctx, tx, "monitor-transition:"+kind); err != nil {
			return err
		}
	}
	for _, transition := range transitions {
		if transition.Suppressed || !monitorTransitionNotifiable(transition.Kind) {
			continue
		}
		if err := enqueueCoalescedMonitorNotification(ctx, tx, ownerJID, transition.Kind, transition.Alert); err != nil {
			return err
		}
	}
	return nil
}

func lockNotificationCoalesce(ctx context.Context, tx *sql.Tx, key string) error {
	var locked string
	if err := tx.QueryRowContext(ctx, `SELECT lock_key FROM echo_notification_coalesce_locks WHERE lock_key=? FOR UPDATE`, key).Scan(&locked); err != nil {
		return fmt.Errorf("lock monitoring notification batch: %w", err)
	}
	return nil
}

func monitorTransitionNotifiable(kind string) bool {
	switch kind {
	case "opened", "reopened", "escalated", "resolved", "maintenance_ended", "reminder":
		return true
	default:
		return false
	}
}

func enqueueCoalescedMonitorNotification(ctx context.Context, tx *sql.Tx, ownerJID, kind string, alert domain.ServiceAlert) error {
	header, item := serviceAlertNotificationParts(kind, alert)
	body := header + "\n\n" + item
	appendItem := "\n\n" + item
	key := "monitor-transition:" + kind
	_, err := tx.ExecContext(ctx, `INSERT INTO echo_whatsapp_outbox(id,chat_jid,body,available_at,coalesce_key,coalesce_count)
      VALUES(?,?,?,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 8 SECOND),?,1)
      ON DUPLICATE KEY UPDATE
        body=CASE
          WHEN coalesce_count<? AND CHAR_LENGTH(body)+CHAR_LENGTH(?)<=? THEN CONCAT(body,?)
          WHEN coalesce_count<=? AND LOCATE(?,body)=0 AND CHAR_LENGTH(body)+CHAR_LENGTH(?)<=? THEN CONCAT(body,?)
          ELSE body
        END,
        coalesce_count=LEAST(coalesce_count+1,1000000)`,
		id.New("out"), ownerJID, body, key,
		monitorNotificationMaxItems, appendItem, monitorNotificationMaxCharacters, appendItem,
		monitorNotificationMaxItems, monitorNotificationOverflow, monitorNotificationOverflow, monitorNotificationMaxCharacters, monitorNotificationOverflow)
	return err
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMonitorEvent(row scanner, item *domain.MonitorEvent) error {
	return row.Scan(monitorEventScanArgs(item)...)
}

func monitorEventScanArgs(item *domain.MonitorEvent) []any {
	return []any{&item.ID, &item.EventID, &item.ServiceID, &item.ServiceName, &item.Source, &item.Status, &item.Severity, &item.Summary, &item.Fingerprint, &item.Details, &item.ObservedAt, &item.ReceivedAt}
}

func scanServiceAlert(row scanner, item *domain.ServiceAlert) error {
	return row.Scan(&item.ID, &item.ServiceID, &item.ServiceName, &item.ProjectID, &item.Fingerprint, &item.Status, &item.Severity, &item.Title, &item.Summary, &item.LatestEventID, &item.OpenedAt, &item.LastSeenAt, &item.ResolvedAt, &item.MaintenanceWindowID, &item.NotificationSuppressedUntil, &item.MaintenanceOpeningSuppressed, &item.AcknowledgedAt, &item.AcknowledgedVia, &item.LastNotifiedAt, &item.ReminderCount)
}

const serviceAlertSelect = `SELECT a.id,a.service_id,s.name,s.project_id,a.fingerprint,a.alert_status,a.severity,a.title,a.summary,a.latest_event_id,a.opened_at,a.last_seen_at,a.resolved_at,COALESCE(a.maintenance_window_id,''),a.notification_suppressed_until,a.maintenance_opening_suppressed,a.acknowledged_at,a.acknowledged_via,a.last_notified_at,a.reminder_count FROM echo_service_alerts a JOIN echo_services s ON s.id=a.service_id`

func updateAlerts(ctx context.Context, tx *sql.Tx, event domain.MonitorEvent) ([]domain.AlertTransition, error) {
	transitions := make([]domain.AlertTransition, 0, 2)
	if event.Source != "echo-watchdog" {
		resolved, err := resolveOpenServiceAlerts(ctx, tx, event, "observer-silent")
		if err != nil {
			return nil, err
		}
		transitions = append(transitions, resolved...)
	}
	if event.Status == "healthy" {
		resolved, err := resolveOpenServiceAlerts(ctx, tx, event, event.Fingerprint)
		if err != nil {
			return nil, err
		}
		transitions = append(transitions, resolved...)
		return transitions, nil
	}
	if event.Status != "degraded" && event.Status != "unhealthy" {
		return transitions, nil
	}
	severity := event.Severity
	if severity != "critical" {
		severity = "warning"
	}
	var alert domain.ServiceAlert
	err := scanServiceAlert(tx.QueryRowContext(ctx, serviceAlertSelect+` WHERE a.service_id=? AND a.fingerprint=? FOR UPDATE`, event.ServiceID, event.Fingerprint), &alert)
	title := alertTitle(event.Summary)
	if errors.Is(err, sql.ErrNoRows) {
		alert = domain.ServiceAlert{ID: id.New("alt"), ServiceID: event.ServiceID, Fingerprint: event.Fingerprint, Status: "open", Severity: severity, Title: title, Summary: event.Summary, LatestEventID: event.EventID, OpenedAt: event.ObservedAt, LastSeenAt: event.ObservedAt}
		if _, err := tx.ExecContext(ctx, `INSERT INTO echo_service_alerts(id,service_id,fingerprint,alert_status,severity,title,summary,latest_event_id,opened_at,last_seen_at) VALUES(?,?,?,'open',?,?,?,?,?,?)`, alert.ID, alert.ServiceID, alert.Fingerprint, alert.Severity, alert.Title, alert.Summary, alert.LatestEventID, alert.OpenedAt, alert.LastSeenAt); err != nil {
			return nil, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT name,project_id FROM echo_services WHERE id=?`, event.ServiceID).Scan(&alert.ServiceName, &alert.ProjectID); err != nil {
			return nil, err
		}
		return append(transitions, domain.AlertTransition{Kind: "opened", Alert: alert}), nil
	}
	if err != nil {
		return nil, err
	}
	if event.ObservedAt.Before(alert.LastSeenAt) {
		return append(transitions, domain.AlertTransition{Kind: "stale", Alert: alert}), nil
	}
	kind := "updated"
	if alert.Status == "resolved" {
		kind = "reopened"
	} else if alert.Severity != "critical" && severity == "critical" {
		kind = "escalated"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET alert_status='open',severity=?,title=?,summary=?,latest_event_id=?,opened_at=IF(alert_status='resolved',?,opened_at),last_seen_at=?,resolved_at=NULL WHERE id=?`, severity, title, event.Summary, event.EventID, event.ObservedAt, event.ObservedAt, alert.ID); err != nil {
		return nil, err
	}
	if kind == "reopened" || kind == "escalated" {
		if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET acknowledged_at=NULL,acknowledged_via='',last_notified_at=NULL,reminder_count=0 WHERE id=?`, alert.ID); err != nil {
			return nil, err
		}
		alert.AcknowledgedAt = nil
		alert.AcknowledgedVia = ""
		alert.LastNotifiedAt = nil
		alert.ReminderCount = 0
	}
	alert.Status = "open"
	alert.Severity = severity
	alert.Title = title
	alert.Summary = event.Summary
	alert.LatestEventID = event.EventID
	alert.LastSeenAt = event.ObservedAt
	alert.ResolvedAt = nil
	if kind == "reopened" {
		alert.OpenedAt = event.ObservedAt
	}
	return append(transitions, domain.AlertTransition{Kind: kind, Alert: alert}), nil
}

func resolveOpenServiceAlerts(ctx context.Context, tx *sql.Tx, event domain.MonitorEvent, fingerprint string) ([]domain.AlertTransition, error) {
	query := serviceAlertSelect + ` WHERE a.service_id=? AND a.alert_status='open' AND a.last_seen_at<=?`
	args := []any{event.ServiceID, event.ObservedAt}
	if fingerprint != "" {
		query += ` AND a.fingerprint=?`
		args = append(args, fingerprint)
	}
	query += ` FOR UPDATE`
	rows, err := tx.QueryContext(ctx, query, args...)
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
		if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET alert_status='resolved',latest_event_id=?,last_seen_at=?,resolved_at=? WHERE id=?`, event.EventID, event.ObservedAt, event.ObservedAt, alert.ID); err != nil {
			return nil, err
		}
		alert.Status = "resolved"
		alert.LatestEventID = event.EventID
		alert.LastSeenAt = event.ObservedAt
		resolved := event.ObservedAt
		alert.ResolvedAt = &resolved
		transitions = append(transitions, domain.AlertTransition{Kind: "resolved", Alert: alert})
	}
	return transitions, nil
}

func applyMaintenancePolicy(ctx context.Context, tx *sql.Tx, transitions []domain.AlertTransition, maintenance *domain.MaintenanceWindow) ([]domain.AlertTransition, error) {
	for index := range transitions {
		transition := &transitions[index]
		alert := &transition.Alert
		if maintenance != nil {
			if alert.Status == "open" {
				openingSuppressed := alert.MaintenanceOpeningSuppressed || transition.Kind == "opened" || transition.Kind == "reopened"
				transition.Suppressed = true
				if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET maintenance_window_id=?,notification_suppressed_until=?,maintenance_opening_suppressed=? WHERE id=?`, maintenance.ID, maintenance.EndsAt, openingSuppressed, alert.ID); err != nil {
					return nil, err
				}
				alert.MaintenanceWindowID = maintenance.ID
				until := maintenance.EndsAt
				alert.NotificationSuppressedUntil = &until
				alert.MaintenanceOpeningSuppressed = openingSuppressed
			} else {
				transition.Suppressed = alert.MaintenanceOpeningSuppressed
				if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET maintenance_window_id=NULL,notification_suppressed_until=NULL,maintenance_opening_suppressed=FALSE WHERE id=?`, alert.ID); err != nil {
					return nil, err
				}
				alert.MaintenanceWindowID = ""
				alert.NotificationSuppressedUntil = nil
				alert.MaintenanceOpeningSuppressed = false
			}
			continue
		}
		if alert.MaintenanceWindowID == "" && alert.NotificationSuppressedUntil == nil {
			continue
		}
		if alert.Status == "open" {
			transition.Kind = "maintenance_ended"
		} else {
			transition.Suppressed = alert.MaintenanceOpeningSuppressed
		}
		if _, err := tx.ExecContext(ctx, `UPDATE echo_service_alerts SET maintenance_window_id=NULL,notification_suppressed_until=NULL,maintenance_opening_suppressed=FALSE WHERE id=?`, alert.ID); err != nil {
			return nil, err
		}
		alert.MaintenanceWindowID = ""
		alert.NotificationSuppressedUntil = nil
		alert.MaintenanceOpeningSuppressed = false
	}
	return transitions, nil
}

func alertTitle(summary string) string {
	summary = strings.TrimSpace(strings.SplitN(summary, "\n", 2)[0])
	runes := []rune(summary)
	if len(runes) > 300 {
		return string(runes[:300])
	}
	return summary
}

func (s *Store) CreateChangeSet(ctx context.Context, item domain.ChangeSet) (domain.ChangeSet, error) {
	if item.ID == "" {
		item.ID = id.New("cng")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO echo_change_sets(id,incident_id,repository_id,branch_name,commit_sha,pull_request_number,pull_request_url,artifact_ref,status,diff_sha256) VALUES(?,?,?,?,?,?,?,?,?,?)`, item.ID, item.IncidentID, item.RepositoryID, item.BranchName, item.CommitSHA, item.PullRequestNumber, item.PullRequestURL, item.ArtifactRef, item.Status, item.DiffSHA256)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE echo_incidents SET status='patching' WHERE id=?`, item.IncidentID)
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	return item, nil
}
func (s *Store) ListChangeSets(ctx context.Context, incidentID string) ([]domain.ChangeSet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,incident_id,repository_id,branch_name,COALESCE(commit_sha,''),COALESCE(pull_request_number,0),pull_request_url,artifact_ref,status,diff_sha256,created_at,updated_at FROM echo_change_sets WHERE incident_id=? ORDER BY created_at DESC`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ChangeSet
	for rows.Next() {
		var item domain.ChangeSet
		if err := rows.Scan(&item.ID, &item.IncidentID, &item.RepositoryID, &item.BranchName, &item.CommitSHA, &item.PullRequestNumber, &item.PullRequestURL, &item.ArtifactRef, &item.Status, &item.DiffSHA256, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetChangeSet(ctx context.Context, changeSetID string) (domain.ChangeSet, error) {
	var item domain.ChangeSet
	err := s.db.QueryRowContext(ctx, `SELECT id,incident_id,repository_id,branch_name,COALESCE(commit_sha,''),COALESCE(pull_request_number,0),pull_request_url,artifact_ref,status,diff_sha256,created_at,updated_at FROM echo_change_sets WHERE id=?`, changeSetID).Scan(&item.ID, &item.IncidentID, &item.RepositoryID, &item.BranchName, &item.CommitSHA, &item.PullRequestNumber, &item.PullRequestURL, &item.ArtifactRef, &item.Status, &item.DiffSHA256, &item.CreatedAt, &item.UpdatedAt)
	return item, notFound(err)
}

func (s *Store) MarkChangeSetMerged(ctx context.Context, changeSetID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE echo_change_sets SET status='merged' WHERE id=? AND status IN ('testing','ready')`, changeSetID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 1 {
		return nil
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM echo_change_sets WHERE id=?`, changeSetID).Scan(&status); err != nil {
		return notFound(err)
	}
	if status == "merged" {
		return nil
	}
	return errors.New("change set is not eligible to be marked merged")
}

func (s *Store) FindChangeSetByArtifact(ctx context.Context, incidentID, artifact string) (domain.ChangeSet, error) {
	var item domain.ChangeSet
	err := s.db.QueryRowContext(ctx, `SELECT id,incident_id,repository_id,branch_name,COALESCE(commit_sha,''),COALESCE(pull_request_number,0),pull_request_url,artifact_ref,status,diff_sha256,created_at,updated_at FROM echo_change_sets WHERE incident_id=? AND artifact_ref=? ORDER BY created_at DESC LIMIT 1`, incidentID, artifact).Scan(&item.ID, &item.IncidentID, &item.RepositoryID, &item.BranchName, &item.CommitSHA, &item.PullRequestNumber, &item.PullRequestURL, &item.ArtifactRef, &item.Status, &item.DiffSHA256, &item.CreatedAt, &item.UpdatedAt)
	return item, notFound(err)
}

func (s *Store) FindProjectChangeSetByArtifact(ctx context.Context, projectID, artifact string) (domain.ChangeSet, error) {
	var item domain.ChangeSet
	err := s.db.QueryRowContext(ctx, `SELECT c.id,c.incident_id,c.repository_id,c.branch_name,COALESCE(c.commit_sha,''),COALESCE(c.pull_request_number,0),c.pull_request_url,c.artifact_ref,c.status,c.diff_sha256,c.created_at,c.updated_at FROM echo_change_sets c JOIN echo_incidents i ON i.id=c.incident_id WHERE i.project_id=? AND c.artifact_ref=? ORDER BY c.created_at DESC LIMIT 1`, projectID, artifact).Scan(&item.ID, &item.IncidentID, &item.RepositoryID, &item.BranchName, &item.CommitSHA, &item.PullRequestNumber, &item.PullRequestURL, &item.ArtifactRef, &item.Status, &item.DiffSHA256, &item.CreatedAt, &item.UpdatedAt)
	return item, notFound(err)
}

func (s *Store) CreateDeployment(ctx context.Context, item domain.Deployment) (domain.Deployment, error) {
	if item.ID == "" {
		item.ID = id.New("dep")
	}
	if item.Operation == "" {
		item.Operation = "deploy"
	}
	if item.Status == "" {
		item.Status = "dispatching"
	}
	var sourceDeploymentID any
	if item.SourceDeploymentID != "" {
		sourceDeploymentID = item.SourceDeploymentID
	}
	var approvalID any
	if item.ApprovalID != "" {
		approvalID = item.ApprovalID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO echo_deployments(id,incident_id,approval_id,project_id,environment_name,operation_type,artifact_ref,source_deployment_id,status,workflow_url,repository_full_name,workflow_name,external_run_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.IncidentID, approvalID, item.ProjectID, item.Environment, item.Operation, item.ArtifactRef, sourceDeploymentID, item.Status, item.WorkflowURL, item.Repository, item.Workflow, item.ExternalRunID)
	if err != nil {
		return domain.Deployment{}, err
	}
	item.CreatedAt = time.Now().UTC()
	return item, nil
}
func (s *Store) ListDeployments(ctx context.Context, incidentID string) ([]domain.Deployment, error) {
	query := `SELECT id,incident_id,COALESCE(approval_id,''),project_id,environment_name,operation_type,artifact_ref,COALESCE(source_deployment_id,''),status,workflow_url,repository_full_name,workflow_name,COALESCE(external_run_id,0),started_at,finished_at,created_at FROM echo_deployments`
	args := []any{}
	if incidentID != "" {
		query += ` WHERE incident_id=?`
		args = append(args, incidentID)
	}
	query += ` ORDER BY created_at DESC LIMIT 200`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Deployment
	for rows.Next() {
		var item domain.Deployment
		if err := rows.Scan(&item.ID, &item.IncidentID, &item.ApprovalID, &item.ProjectID, &item.Environment, &item.Operation, &item.ArtifactRef, &item.SourceDeploymentID, &item.Status, &item.WorkflowURL, &item.Repository, &item.Workflow, &item.ExternalRunID, &item.StartedAt, &item.FinishedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListProjectDeployments(ctx context.Context, projectID string, limit int) ([]domain.Deployment, error) {
	if projectID == "" {
		return nil, errors.New("project ID is required")
	}
	if limit < 1 || limit > 201 {
		limit = 201
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,incident_id,COALESCE(approval_id,''),project_id,environment_name,operation_type,artifact_ref,COALESCE(source_deployment_id,''),status,workflow_url,repository_full_name,workflow_name,COALESCE(external_run_id,0),started_at,finished_at,created_at
	  FROM echo_deployments WHERE project_id=? ORDER BY created_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Deployment
	for rows.Next() {
		var item domain.Deployment
		if err := rows.Scan(&item.ID, &item.IncidentID, &item.ApprovalID, &item.ProjectID, &item.Environment, &item.Operation, &item.ArtifactRef, &item.SourceDeploymentID, &item.Status, &item.WorkflowURL, &item.Repository, &item.Workflow, &item.ExternalRunID, &item.StartedAt, &item.FinishedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetDeployment(ctx context.Context, deploymentID string) (domain.Deployment, error) {
	var item domain.Deployment
	err := s.db.QueryRowContext(ctx, `SELECT id,incident_id,COALESCE(approval_id,''),project_id,environment_name,operation_type,artifact_ref,COALESCE(source_deployment_id,''),status,workflow_url,repository_full_name,workflow_name,COALESCE(external_run_id,0),started_at,finished_at,created_at FROM echo_deployments WHERE id=?`, deploymentID).Scan(&item.ID, &item.IncidentID, &item.ApprovalID, &item.ProjectID, &item.Environment, &item.Operation, &item.ArtifactRef, &item.SourceDeploymentID, &item.Status, &item.WorkflowURL, &item.Repository, &item.Workflow, &item.ExternalRunID, &item.StartedAt, &item.FinishedAt, &item.CreatedAt)
	return item, notFound(err)
}

func (s *Store) FindDeploymentByApproval(ctx context.Context, approvalID string) (domain.Deployment, error) {
	var item domain.Deployment
	err := s.db.QueryRowContext(ctx, `SELECT id,incident_id,COALESCE(approval_id,''),project_id,environment_name,operation_type,artifact_ref,COALESCE(source_deployment_id,''),status,workflow_url,repository_full_name,workflow_name,COALESCE(external_run_id,0),started_at,finished_at,created_at FROM echo_deployments WHERE approval_id=?`, approvalID).Scan(&item.ID, &item.IncidentID, &item.ApprovalID, &item.ProjectID, &item.Environment, &item.Operation, &item.ArtifactRef, &item.SourceDeploymentID, &item.Status, &item.WorkflowURL, &item.Repository, &item.Workflow, &item.ExternalRunID, &item.StartedAt, &item.FinishedAt, &item.CreatedAt)
	return item, notFound(err)
}

func (s *Store) MarkDeploymentQueued(ctx context.Context, deploymentID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE echo_deployments SET status='queued' WHERE id=? AND status='dispatching'`, deploymentID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CompleteRepositoryNativeDeployment(ctx context.Context, deploymentID string, runID int64, runURL string, success bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var incidentID string
	if err := tx.QueryRowContext(ctx, `SELECT incident_id FROM echo_deployments WHERE id=? FOR UPDATE`, deploymentID).Scan(&incidentID); err != nil {
		return notFound(err)
	}
	status := "failed"
	incidentStatus := "failed"
	if success {
		status = "verified"
		incidentStatus = "resolved"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_deployments SET status=?,workflow_url=?,external_run_id=?,started_at=COALESCE(started_at,created_at),finished_at=NOW(6) WHERE id=?`, status, runURL, runID, deploymentID); err != nil {
		return err
	}
	if success {
		_, err = tx.ExecContext(ctx, `UPDATE echo_incidents SET status=?,resolved_at=NOW(6) WHERE id=?`, incidentStatus, incidentID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE echo_incidents SET status=?,resolved_at=NULL WHERE id=?`, incidentStatus, incidentID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteDeploymentFromWorkflow(ctx context.Context, repository, workflow, artifact string, runID int64, runURL string, success bool) (domain.Deployment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Deployment{}, err
	}
	defer tx.Rollback()
	var item domain.Deployment
	err = tx.QueryRowContext(ctx, `SELECT id,incident_id,COALESCE(approval_id,''),project_id,environment_name,operation_type,artifact_ref,COALESCE(source_deployment_id,''),status,workflow_url,repository_full_name,workflow_name,COALESCE(external_run_id,0),started_at,finished_at,created_at FROM echo_deployments WHERE repository_full_name=? AND workflow_name=? AND artifact_ref=? AND status IN ('dispatching','queued','running') ORDER BY created_at LIMIT 1 FOR UPDATE`, repository, workflow, artifact).Scan(&item.ID, &item.IncidentID, &item.ApprovalID, &item.ProjectID, &item.Environment, &item.Operation, &item.ArtifactRef, &item.SourceDeploymentID, &item.Status, &item.WorkflowURL, &item.Repository, &item.Workflow, &item.ExternalRunID, &item.StartedAt, &item.FinishedAt, &item.CreatedAt)
	if err != nil {
		return domain.Deployment{}, notFound(err)
	}
	status := "failed"
	if success {
		status = "verified"
	}
	_, err = tx.ExecContext(ctx, `UPDATE echo_deployments SET status=?,external_run_id=?,workflow_url=?,started_at=COALESCE(started_at,created_at),finished_at=NOW(6) WHERE id=?`, status, runID, runURL, item.ID)
	if err != nil {
		return domain.Deployment{}, err
	}
	if success && item.Operation == "rollback" {
		if item.SourceDeploymentID == "" {
			return domain.Deployment{}, errors.New("rollback deployment has no source deployment")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE echo_deployments SET status='rolled_back' WHERE id=? AND status='verified'`, item.SourceDeploymentID); err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE echo_incidents SET status='resolved',resolved_at=NOW(6) WHERE id=?`, item.IncidentID)
		}
	} else if success && strings.EqualFold(item.Environment, "staging") {
		_, err = tx.ExecContext(ctx, `UPDATE echo_incidents SET status='staging_verified' WHERE id=?`, item.IncidentID)
	} else if success && strings.EqualFold(item.Environment, "production") {
		_, err = tx.ExecContext(ctx, `UPDATE echo_incidents SET status='resolved',resolved_at=NOW(6) WHERE id=?`, item.IncidentID)
	} else if !success {
		_, err = tx.ExecContext(ctx, `UPDATE echo_incidents SET status='failed' WHERE id=?`, item.IncidentID)
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Deployment{}, err
	}
	item.Status = status
	item.ExternalRunID = runID
	item.WorkflowURL = runURL
	now := time.Now().UTC()
	item.FinishedAt = &now
	return item, nil
}

func (s *Store) ListIncidents(ctx context.Context, limit int) ([]domain.Incident, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.project_id,p.name,i.status,i.severity,i.summary,i.reporter_jid,i.reporter_name,i.group_jid,i.source_message_id,i.source_body,i.evidence_revision,i.assigned_to,i.created_at,i.updated_at,i.resolved_at FROM echo_incidents i JOIN echo_projects p ON p.id=i.project_id ORDER BY i.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Incident
	for rows.Next() {
		var item domain.Incident
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.ProjectName, &item.Status, &item.Severity, &item.Summary, &item.ReporterJID, &item.ReporterName, &item.GroupJID, &item.SourceMessageID, &item.SourceBody, &item.EvidenceRevision, &item.AssignedTo, &item.CreatedAt, &item.UpdatedAt, &item.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListProjectIncidents(ctx context.Context, projectID string, limit int) ([]domain.Incident, error) {
	if projectID == "" {
		return nil, errors.New("project ID is required")
	}
	if limit < 1 || limit > 101 {
		limit = 101
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.project_id,p.name,i.status,i.severity,i.summary,i.reporter_jid,i.reporter_name,i.group_jid,i.source_message_id,i.source_body,i.evidence_revision,i.assigned_to,i.created_at,i.updated_at,i.resolved_at
	  FROM echo_incidents i JOIN echo_projects p ON p.id=i.project_id
	  WHERE i.project_id=? ORDER BY i.created_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Incident
	for rows.Next() {
		var item domain.Incident
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.ProjectName, &item.Status, &item.Severity, &item.Summary, &item.ReporterJID, &item.ReporterName, &item.GroupJID, &item.SourceMessageID, &item.SourceBody, &item.EvidenceRevision, &item.AssignedTo, &item.CreatedAt, &item.UpdatedAt, &item.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetIncident(ctx context.Context, incidentID string) (domain.Incident, error) {
	var item domain.Incident
	err := s.db.QueryRowContext(ctx, `SELECT i.id,i.project_id,p.name,i.status,i.severity,i.summary,i.reporter_jid,i.reporter_name,i.group_jid,i.source_message_id,i.source_body,i.evidence_revision,i.assigned_to,i.created_at,i.updated_at,i.resolved_at FROM echo_incidents i JOIN echo_projects p ON p.id=i.project_id WHERE i.id=?`, incidentID).Scan(&item.ID, &item.ProjectID, &item.ProjectName, &item.Status, &item.Severity, &item.Summary, &item.ReporterJID, &item.ReporterName, &item.GroupJID, &item.SourceMessageID, &item.SourceBody, &item.EvidenceRevision, &item.AssignedTo, &item.CreatedAt, &item.UpdatedAt, &item.ResolvedAt)
	return item, notFound(err)
}

func (s *Store) FindIncidentBySource(ctx context.Context, groupJID, messageID string) (domain.Incident, error) {
	var incidentID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM echo_incidents WHERE group_jid=? AND source_message_id=?`, groupJID, messageID).Scan(&incidentID); err != nil {
		return domain.Incident{}, notFound(err)
	}
	return s.GetIncident(ctx, incidentID)
}

func (s *Store) CreateIncident(ctx context.Context, binding domain.GroupBinding, msg domain.InboundMessage) (domain.Incident, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Incident{}, false, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM echo_incidents WHERE group_jid=? AND source_message_id=?`, msg.ChatJID, msg.MessageID).Scan(&existing)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return domain.Incident{}, false, err
		}
		item, e := s.GetIncident(ctx, existing)
		return item, false, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Incident{}, false, err
	}
	incidentID := id.New("inc")
	summary := strings.TrimSpace(msg.Body)
	if len(summary) > 500 {
		summary = summary[:500]
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO echo_incidents(id,project_id,summary,reporter_jid,reporter_name,group_jid,source_message_id,source_body) VALUES(?,?,?,?,?,?,?,?)`, incidentID, binding.ProjectID, summary, msg.SenderJID, msg.SenderName, msg.ChatJID, msg.MessageID, msg.Body)
	if err != nil {
		return domain.Incident{}, false, err
	}
	metadata, _ := json.Marshal(map[string]any{"mentionedJids": msg.MentionedJIDs, "isGroup": msg.IsGroup, "replyToMessageId": msg.ReplyToMessageID, "attachmentCount": len(msg.Attachments)})
	_, err = tx.ExecContext(ctx, `INSERT INTO echo_incident_messages(id,incident_id,whatsapp_message_id,chat_jid,sender_jid,sender_name,direction,body,metadata_json,message_timestamp) VALUES(?,?,?,?,?,?,'inbound',?,?,?)`, id.New("msg"), incidentID, msg.MessageID, msg.ChatJID, msg.SenderJID, msg.SenderName, msg.Body, metadata, msg.Timestamp)
	if err != nil {
		return domain.Incident{}, false, err
	}
	if err = insertIncidentAttachments(ctx, tx, incidentID, msg.MessageID, msg.Attachments); err != nil {
		return domain.Incident{}, false, err
	}
	jobPayload, _ := json.Marshal(map[string]string{"incidentId": incidentID})
	_, err = tx.ExecContext(ctx, `INSERT INTO echo_jobs(id,job_type,payload_json) VALUES(?,'analyze_incident',?)`, id.New("job"), jobPayload)
	if err != nil {
		return domain.Incident{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO echo_whatsapp_outbox(id,chat_jid,body) VALUES(?,?,?)`, id.New("out"), msg.ChatJID, binding.AckTemplate)
	if err != nil {
		return domain.Incident{}, false, err
	}
	if err = insertAudit(ctx, tx, "whatsapp", msg.SenderJID, "incident.reported", "incident", incidentID, "succeeded", map[string]any{"groupJid": msg.ChatJID, "attachmentCount": len(msg.Attachments)}); err != nil {
		return domain.Incident{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Incident{}, false, err
	}
	item, e := s.GetIncident(ctx, incidentID)
	return item, true, e
}

func (s *Store) SaveAnalysis(ctx context.Context, item domain.Analysis) (domain.Analysis, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Analysis{}, err
	}
	defer tx.Rollback()
	if item.ID == "" {
		item.ID = id.New("ana")
	}
	if len(item.Evidence) == 0 {
		item.Evidence = json.RawMessage(`[]`)
	}
	if len(item.Proposal) == 0 {
		item.Proposal = json.RawMessage(`{}`)
	}
	if item.EvidenceRevision == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT evidence_revision FROM echo_incidents WHERE id=?`, item.IncidentID).Scan(&item.EvidenceRevision); err != nil {
			return domain.Analysis{}, notFound(err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO echo_analyses(id,incident_id,evidence_revision,status,confidence,summary,evidence_json,proposal_json,model) VALUES(?,?,?,?,?,?,?,?,?)`, item.ID, item.IncidentID, item.EvidenceRevision, item.Status, item.Confidence, item.Summary, item.Evidence, item.Proposal, item.Model)
	if err != nil {
		return domain.Analysis{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE echo_incidents SET status='awaiting_owner' WHERE id=? AND evidence_revision=? AND status IN ('reported','triaging')`, item.IncidentID, item.EvidenceRevision); err != nil {
		return domain.Analysis{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Analysis{}, err
	}
	item.CreatedAt = time.Now().UTC()
	return item, nil
}

func (s *Store) LatestAnalysis(ctx context.Context, incidentID string) (domain.Analysis, error) {
	var item domain.Analysis
	err := s.db.QueryRowContext(ctx, `SELECT id,incident_id,evidence_revision,status,confidence,summary,evidence_json,proposal_json,model,created_at FROM echo_analyses WHERE incident_id=? ORDER BY evidence_revision DESC,created_at DESC LIMIT 1`, incidentID).Scan(&item.ID, &item.IncidentID, &item.EvidenceRevision, &item.Status, &item.Confidence, &item.Summary, &item.Evidence, &item.Proposal, &item.Model, &item.CreatedAt)
	return item, notFound(err)
}

func (s *Store) ListApprovals(ctx context.Context) ([]domain.Approval, error) {
	_, _ = s.db.ExecContext(ctx, `UPDATE echo_approvals SET status='expired' WHERE status='pending' AND expires_at<=NOW(6)`)
	rows, err := s.db.QueryContext(ctx, `SELECT id,incident_id,action_type,action_hash,action_json,status,requested_by,resolved_by,expires_at,created_at,resolved_at FROM echo_approvals ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Approval
	for rows.Next() {
		var item domain.Approval
		if err := rows.Scan(&item.ID, &item.IncidentID, &item.ActionType, &item.ActionHash, &item.Action, &item.Status, &item.RequestedBy, &item.ResolvedBy, &item.ExpiresAt, &item.CreatedAt, &item.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CreateApproval(ctx context.Context, item domain.Approval, nonceHash string) (domain.Approval, error) {
	if item.ID == "" {
		item.ID = id.New("apr")
	}
	canonical := sha256.Sum256(item.Action)
	item.ActionHash = hex.EncodeToString(canonical[:])
	_, err := s.db.ExecContext(ctx, `INSERT INTO echo_approvals(id,incident_id,action_type,action_hash,action_json,nonce_hash,requested_by,expires_at) VALUES(?,?,?,?,?,?,?,?)`, item.ID, item.IncidentID, item.ActionType, item.ActionHash, item.Action, nonceHash, item.RequestedBy, item.ExpiresAt)
	if err != nil {
		return domain.Approval{}, err
	}
	item.Status = "pending"
	item.CreatedAt = time.Now().UTC()
	return item, nil
}

func (s *Store) ResolveApproval(ctx context.Context, approvalID, resolver, status string) (domain.Approval, error) {
	if status != "approved" && status != "rejected" {
		return domain.Approval{}, errors.New("invalid approval resolution")
	}
	var targetIncidentID string
	if err := s.db.QueryRowContext(ctx, `SELECT incident_id FROM echo_approvals WHERE id=?`, approvalID).Scan(&targetIncidentID); err != nil {
		return domain.Approval{}, notFound(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback()
	var currentRevision uint64
	if err = tx.QueryRowContext(ctx, `SELECT evidence_revision FROM echo_incidents WHERE id=? FOR UPDATE`, targetIncidentID).Scan(&currentRevision); err != nil {
		return domain.Approval{}, err
	}
	var item domain.Approval
	err = tx.QueryRowContext(ctx, `SELECT id,incident_id,action_type,action_hash,action_json,status,requested_by,resolved_by,expires_at,created_at,resolved_at FROM echo_approvals WHERE id=? AND status='pending' AND expires_at>NOW(6) FOR UPDATE`, approvalID).Scan(&item.ID, &item.IncidentID, &item.ActionType, &item.ActionHash, &item.Action, &item.Status, &item.RequestedBy, &item.ResolvedBy, &item.ExpiresAt, &item.CreatedAt, &item.ResolvedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Approval{}, errors.New("approval is not pending or has expired")
		}
		return domain.Approval{}, err
	}
	if status == "approved" && item.ActionType == "prepare_patch" {
		var action struct {
			EvidenceRevision uint64 `json:"evidenceRevision"`
		}
		if err = json.Unmarshal(item.Action, &action); err != nil {
			return domain.Approval{}, err
		}
		if action.EvidenceRevision == 0 || action.EvidenceRevision != currentRevision {
			if _, err = tx.ExecContext(ctx, `UPDATE echo_approvals SET status='expired',resolved_by='system:evidence_changed',resolved_at=NOW(6) WHERE id=?`, approvalID); err != nil {
				return domain.Approval{}, err
			}
			if err = tx.Commit(); err != nil {
				return domain.Approval{}, err
			}
			return domain.Approval{}, ErrApprovalStale
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE echo_approvals SET status=?,resolved_by=?,resolved_at=NOW(6) WHERE id=?`, status, resolver, approvalID); err != nil {
		return domain.Approval{}, err
	}
	if status == "approved" {
		nextStatus := ""
		switch item.ActionType {
		case "prepare_patch":
			nextStatus = "change_approved"
		case "deploy_staging":
			nextStatus = "staging"
		case "deploy_production", "merge_production":
			nextStatus = "production"
		}
		if nextStatus != "" {
			if _, err = tx.ExecContext(ctx, `UPDATE echo_incidents SET status=? WHERE id=?`, nextStatus, item.IncidentID); err != nil {
				return domain.Approval{}, err
			}
		}
		payload, _ := json.Marshal(map[string]any{"incidentId": item.IncidentID, "approvalId": approvalID, "action": json.RawMessage(item.Action)})
		if _, err = tx.ExecContext(ctx, `INSERT INTO echo_jobs(id,job_type,payload_json) VALUES(?,?,?)`, id.New("job"), item.ActionType, payload); err != nil {
			return domain.Approval{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return domain.Approval{}, err
	}
	item.Status = status
	item.ResolvedBy = resolver
	now := time.Now().UTC()
	item.ResolvedAt = &now
	return item, nil
}

func (s *Store) FindPendingApprovalByNonceHash(ctx context.Context, nonceHash string) (string, error) {
	var approvalID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM echo_approvals WHERE nonce_hash=? AND status='pending' AND expires_at>NOW(6) ORDER BY created_at DESC LIMIT 1`, nonceHash).Scan(&approvalID)
	return approvalID, notFound(err)
}

func (s *Store) EnqueueOutbound(ctx context.Context, chatJID, body string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO echo_whatsapp_outbox(id,chat_jid,body) VALUES(?,?,?)`, id.New("out"), chatJID, body)
	return err
}

func (s *Store) ClaimOutbound(ctx context.Context, lease time.Duration) (domain.OutboundMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.OutboundMessage{}, err
	}
	defer tx.Rollback()
	var item domain.OutboundMessage
	err = tx.QueryRowContext(ctx, `SELECT id,chat_jid,body,status,attempts,created_at FROM echo_whatsapp_outbox
      WHERE (((status IN ('queued','failed')) AND available_at<=NOW(6) AND (locked_until IS NULL OR locked_until<NOW(6)))
         OR (status='sending' AND locked_until<NOW(6)))
        AND attempts<=5
      ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&item.ID, &item.ChatJID, &item.Body, &item.Status, &item.Attempts, &item.CreatedAt)
	if err != nil {
		return domain.OutboundMessage{}, notFound(err)
	}
	if item.Attempts >= 5 {
		if _, err = tx.ExecContext(ctx, `UPDATE echo_whatsapp_outbox SET status='dead',locked_until=NULL,last_error=IF(last_error='','delivery lease expired after final attempt',last_error) WHERE id=?`, item.ID); err != nil {
			return domain.OutboundMessage{}, err
		}
		if err = tx.Commit(); err != nil {
			return domain.OutboundMessage{}, err
		}
		return domain.OutboundMessage{}, ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `UPDATE echo_whatsapp_outbox SET status='sending',attempts=attempts+1,locked_until=?,coalesce_key=NULL WHERE id=?`, time.Now().Add(lease), item.ID)
	if err != nil {
		return domain.OutboundMessage{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.OutboundMessage{}, err
	}
	item.Status = "sending"
	item.Attempts++
	return item, nil
}

func (s *Store) CompleteOutbound(ctx context.Context, outboxID string, attempt int, messageID string, sendErr error) error {
	if sendErr == nil {
		_, err := s.db.ExecContext(ctx, `UPDATE echo_whatsapp_outbox SET status='sent',whatsapp_message_id=?,sent_at=NOW(6),locked_until=NULL WHERE id=? AND status='sending' AND attempts=?`, messageID, outboxID, attempt)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE echo_whatsapp_outbox SET status=IF(attempts>=5,'dead','failed'),last_error=?,available_at=DATE_ADD(NOW(6),INTERVAL LEAST(attempts*attempts,60) SECOND),locked_until=NULL WHERE id=? AND status='sending' AND attempts=?`, truncate(sendErr.Error(), 1000), outboxID, attempt)
	return err
}

func (s *Store) ClaimJob(ctx context.Context, worker string, lease time.Duration) (domain.Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, err
	}
	defer tx.Rollback()
	var item domain.Job
	err = tx.QueryRowContext(ctx, `SELECT id,job_type,payload_json,status,attempts,max_attempts,locked_by,locked_until,created_at FROM echo_jobs
      WHERE (((status IN ('queued','failed')) AND available_at<=NOW(6) AND (locked_until IS NULL OR locked_until<NOW(6)))
         OR (status='running' AND locked_until<NOW(6)))
        AND attempts<=max_attempts
      ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&item.ID, &item.Type, &item.Payload, &item.Status, &item.Attempts, &item.MaxAttempts, &item.LockedBy, &item.LockedUntil, &item.CreatedAt)
	if err != nil {
		return domain.Job{}, notFound(err)
	}
	if item.Attempts >= item.MaxAttempts {
		if _, err = tx.ExecContext(ctx, `UPDATE echo_jobs SET status='dead',locked_by='',locked_until=NULL,last_error=IF(last_error='','job lease expired after final attempt',last_error) WHERE id=?`, item.ID); err != nil {
			return domain.Job{}, err
		}
		if err = tx.Commit(); err != nil {
			return domain.Job{}, err
		}
		return domain.Job{}, ErrNotFound
	}
	lockedUntil := time.Now().Add(lease)
	_, err = tx.ExecContext(ctx, `UPDATE echo_jobs SET status='running',attempts=attempts+1,locked_by=?,locked_until=? WHERE id=?`, worker, lockedUntil, item.ID)
	if err != nil {
		return domain.Job{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Job{}, err
	}
	item.Status = "running"
	item.Attempts++
	item.LockedBy = worker
	item.LockedUntil = &lockedUntil
	return item, nil
}

func (s *Store) RenewJobLease(ctx context.Context, jobID string, attempt int, worker string, lease time.Duration) (bool, error) {
	if lease <= 0 {
		return false, errors.New("job lease must be positive")
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE echo_jobs SET locked_until=? WHERE id=? AND status='running' AND attempts=? AND locked_by=?`, now.Add(lease), jobID, attempt, worker)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO echo_worker_heartbeats(worker_id,started_at,last_seen_at) VALUES(?,?,?)
      ON DUPLICATE KEY UPDATE last_seen_at=GREATEST(last_seen_at,VALUES(last_seen_at))`, worker, now, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) CompleteJob(ctx context.Context, jobID string, attempt int, worker string, jobErr error, terminalNotification *domain.OutboundMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var result sql.Result
	if jobErr == nil {
		result, err = tx.ExecContext(ctx, `UPDATE echo_jobs SET status='complete',locked_by='',locked_until=NULL,last_error='' WHERE id=? AND status='running' AND attempts=? AND locked_by=?`, jobID, attempt, worker)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE echo_jobs SET status=IF(attempts>=max_attempts,'dead','failed'),locked_by='',locked_until=NULL,last_error=?,available_at=DATE_ADD(NOW(6),INTERVAL LEAST(attempts*attempts,300) SECOND) WHERE id=? AND status='running' AND attempts=? AND locked_by=?`, truncate(jobErr.Error(), 1000), jobID, attempt, worker)
	}
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 && jobErr != nil && terminalNotification != nil {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM echo_jobs WHERE id=?`, jobID).Scan(&status); err != nil {
			return err
		}
		if status == "dead" {
			chatJID := strings.TrimSpace(terminalNotification.ChatJID)
			body := strings.TrimSpace(terminalNotification.Body)
			if chatJID == "" || body == "" {
				return errors.New("terminal job notification requires chat JID and body")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO echo_whatsapp_outbox(id,chat_jid,body) VALUES(?,?,?)`, id.New("out"), chatJID, body); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) ListJobs(ctx context.Context, status string, limit int) ([]domain.Job, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query := `SELECT id,job_type,payload_json,status,attempts,max_attempts,available_at,locked_by,locked_until,last_error,created_at,updated_at FROM echo_jobs`
	args := []any{}
	if status == "queued" || status == "running" || status == "failed" || status == "dead" || status == "complete" {
		query += ` WHERE status=?`
		args = append(args, status)
	}
	query += ` ORDER BY FIELD(status,'dead','failed','running','queued','complete'),updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Job
	for rows.Next() {
		var item domain.Job
		if err := rows.Scan(&item.ID, &item.Type, &item.Payload, &item.Status, &item.Attempts, &item.MaxAttempts, &item.AvailableAt, &item.LockedBy, &item.LockedUntil, &item.LastError, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.IncidentID = domain.JobIncidentID(item.Payload)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Audit(ctx context.Context, actorType, actorID, action, resourceType, resourceID, outcome string, metadata any) error {
	return insertAudit(ctx, s.db, actorType, actorID, action, resourceType, resourceID, outcome, metadata)
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,actor_type,actor_id,action_name,resource_type,resource_id,outcome,metadata_json,created_at FROM echo_audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AuditEvent
	for rows.Next() {
		var item domain.AuditEvent
		if err := rows.Scan(&item.ID, &item.ActorType, &item.ActorID, &item.Action, &item.ResourceType, &item.ResourceID, &item.Outcome, &item.Metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertAudit(ctx context.Context, e execer, actorType, actorID, action, resourceType, resourceID, outcome string, metadata any) error {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = e.ExecContext(ctx, `INSERT INTO echo_audit_events(actor_type,actor_id,action_name,resource_type,resource_id,outcome,metadata_json) VALUES(?,?,?,?,?,?,?)`, actorType, actorID, action, resourceType, resourceID, outcome, raw)
	return err
}
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func defaultDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}
func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
