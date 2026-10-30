package store

import (
	"context"
	"errors"
	"strings"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
	"github.com/go-sql-driver/mysql"
)

var ErrDependencyExists = errors.New("service dependency already exists")
var ErrDependencyProjectMismatch = errors.New("service dependencies must stay within one project")
var ErrServiceHasDependencies = errors.New("service project cannot change while dependencies exist")

func (s *Store) ListServiceDependencies(ctx context.Context, serviceID string, limit int) ([]domain.ServiceDependency, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	query := serviceDependencySelect
	args := []any{}
	if serviceID = strings.TrimSpace(serviceID); serviceID != "" {
		query += ` WHERE d.service_id=? OR d.depends_on_service_id=?`
		args = append(args, serviceID, serviceID)
	}
	query += ` ORDER BY source.name,target.name,d.id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ServiceDependency, 0, min(limit, 200))
	for rows.Next() {
		var item domain.ServiceDependency
		if err := scanServiceDependency(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateServiceDependency(ctx context.Context, item domain.ServiceDependency) (domain.ServiceDependency, error) {
	item.ServiceID = strings.TrimSpace(item.ServiceID)
	item.DependsOnServiceID = strings.TrimSpace(item.DependsOnServiceID)
	item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
	item.Description = strings.Join(strings.Fields(item.Description), " ")
	if len(item.ServiceID) != 28 || len(item.DependsOnServiceID) != 28 || item.ServiceID == item.DependsOnServiceID || !validDependencyKind(item.Kind) || len(item.Description) > 300 {
		return domain.ServiceDependency{}, errors.New("invalid service dependency")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ServiceDependency{}, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id,project_id FROM echo_services WHERE id IN (?,?) ORDER BY id FOR UPDATE`, item.ServiceID, item.DependsOnServiceID)
	if err != nil {
		return domain.ServiceDependency{}, err
	}
	projects := map[string]string{}
	for rows.Next() {
		var serviceID, projectID string
		if err := rows.Scan(&serviceID, &projectID); err != nil {
			_ = rows.Close()
			return domain.ServiceDependency{}, err
		}
		projects[serviceID] = projectID
	}
	if err := rows.Close(); err != nil {
		return domain.ServiceDependency{}, err
	}
	if len(projects) != 2 {
		return domain.ServiceDependency{}, ErrNotFound
	}
	if projects[item.ServiceID] != projects[item.DependsOnServiceID] {
		return domain.ServiceDependency{}, ErrDependencyProjectMismatch
	}
	item.ID = id.New("dep")
	_, err = tx.ExecContext(ctx, `INSERT INTO echo_service_dependencies(id,service_id,depends_on_service_id,dependency_kind,description) VALUES(?,?,?,?,?)`, item.ID, item.ServiceID, item.DependsOnServiceID, item.Kind, item.Description)
	if duplicateKey(err) {
		return domain.ServiceDependency{}, ErrDependencyExists
	}
	if err != nil {
		return domain.ServiceDependency{}, err
	}
	if err := scanServiceDependency(tx.QueryRowContext(ctx, serviceDependencySelect+` WHERE d.id=?`, item.ID), &item); err != nil {
		return domain.ServiceDependency{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ServiceDependency{}, err
	}
	return item, nil
}

func (s *Store) DeleteServiceDependency(ctx context.Context, dependencyID string) (domain.ServiceDependency, error) {
	if len(dependencyID) != 28 {
		return domain.ServiceDependency{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ServiceDependency{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var item domain.ServiceDependency
	if err := scanServiceDependency(tx.QueryRowContext(ctx, serviceDependencySelect+` WHERE d.id=? FOR UPDATE`, dependencyID), &item); err != nil {
		return domain.ServiceDependency{}, notFound(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM echo_service_dependencies WHERE id=?`, item.ID); err != nil {
		return domain.ServiceDependency{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ServiceDependency{}, err
	}
	return item, nil
}

const serviceDependencySelect = `SELECT d.id,source.project_id,d.service_id,source.service_key,source.name,d.depends_on_service_id,target.service_key,target.name,d.dependency_kind,d.description,d.created_at
  FROM echo_service_dependencies d
  JOIN echo_services source ON source.id=d.service_id
  JOIN echo_services target ON target.id=d.depends_on_service_id`

func scanServiceDependency(row scanner, item *domain.ServiceDependency) error {
	return row.Scan(&item.ID, &item.ProjectID, &item.ServiceID, &item.ServiceKey, &item.ServiceName, &item.DependsOnServiceID, &item.DependsOnServiceKey, &item.DependsOnServiceName, &item.Kind, &item.Description, &item.CreatedAt)
}

func validDependencyKind(value string) bool {
	return value == "runtime" || value == "data" || value == "messaging" || value == "network" || value == "other"
}

func duplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
