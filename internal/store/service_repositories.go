package store

import (
	"context"
	"errors"
	"strings"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

var ErrInvalidServiceRepository = errors.New("invalid service repository link")
var ErrServiceRepositoryExists = errors.New("service repository link already exists")
var ErrServiceRepositoryProjectMismatch = errors.New("service and repository must belong to the same project")
var ErrRepositoryHasServiceLinks = errors.New("repository project cannot change while service links exist")

func (s *Store) ListServiceRepositories(ctx context.Context, serviceID string, limit int) ([]domain.ServiceRepository, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	query := serviceRepositorySelect
	args := []any{}
	if serviceID = strings.TrimSpace(serviceID); serviceID != "" {
		query += ` WHERE l.service_id=?`
		args = append(args, serviceID)
	}
	query += ` ORDER BY s.name,r.owner_name,r.repository_name,l.id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ServiceRepository, 0, min(limit, 200))
	for rows.Next() {
		var item domain.ServiceRepository
		if err := scanServiceRepository(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListServiceRepositoryCoverage(ctx context.Context) ([]domain.ServiceRepositoryCoverage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id,s.project_id,s.enabled,COUNT(l.id),COALESCE(SUM(CASE WHEN r.enabled=TRUE THEN 1 ELSE 0 END),0)
  FROM echo_services s
  LEFT JOIN echo_service_repositories l ON l.service_id=s.id
  LEFT JOIN echo_repositories r ON r.id=l.repository_id
  GROUP BY s.id,s.project_id,s.enabled
  ORDER BY s.project_id,s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ServiceRepositoryCoverage, 0)
	for rows.Next() {
		var item domain.ServiceRepositoryCoverage
		if err := rows.Scan(&item.ServiceID, &item.ProjectID, &item.ServiceEnabled, &item.RepositoryCount, &item.EnabledRepositoryCount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateServiceRepository(ctx context.Context, item domain.ServiceRepository) (domain.ServiceRepository, error) {
	item.ServiceID = strings.TrimSpace(item.ServiceID)
	item.RepositoryID = strings.TrimSpace(item.RepositoryID)
	if len(item.ServiceID) != 28 || len(item.RepositoryID) != 29 {
		return domain.ServiceRepository{}, ErrInvalidServiceRepository
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ServiceRepository{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var serviceProjectID, repositoryProjectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM echo_services WHERE id=? FOR UPDATE`, item.ServiceID).Scan(&serviceProjectID); err != nil {
		return domain.ServiceRepository{}, notFound(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM echo_repositories WHERE id=? FOR UPDATE`, item.RepositoryID).Scan(&repositoryProjectID); err != nil {
		return domain.ServiceRepository{}, notFound(err)
	}
	if serviceProjectID != repositoryProjectID {
		return domain.ServiceRepository{}, ErrServiceRepositoryProjectMismatch
	}
	item.ID = id.New("srl")
	if _, err := tx.ExecContext(ctx, `INSERT INTO echo_service_repositories(id,service_id,repository_id) VALUES(?,?,?)`, item.ID, item.ServiceID, item.RepositoryID); duplicateKey(err) {
		return domain.ServiceRepository{}, ErrServiceRepositoryExists
	} else if err != nil {
		return domain.ServiceRepository{}, err
	}
	if err := scanServiceRepository(tx.QueryRowContext(ctx, serviceRepositorySelect+` WHERE l.id=?`, item.ID), &item); err != nil {
		return domain.ServiceRepository{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ServiceRepository{}, err
	}
	return item, nil
}

func (s *Store) DeleteServiceRepository(ctx context.Context, linkID string) (domain.ServiceRepository, error) {
	if len(strings.TrimSpace(linkID)) != 28 {
		return domain.ServiceRepository{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ServiceRepository{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var item domain.ServiceRepository
	if err := scanServiceRepository(tx.QueryRowContext(ctx, serviceRepositorySelect+` WHERE l.id=? FOR UPDATE`, linkID), &item); err != nil {
		return domain.ServiceRepository{}, notFound(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM echo_service_repositories WHERE id=?`, item.ID); err != nil {
		return domain.ServiceRepository{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ServiceRepository{}, err
	}
	return item, nil
}

const serviceRepositorySelect = `SELECT l.id,s.project_id,l.service_id,s.service_key,s.name,l.repository_id,CONCAT(r.owner_name,'/',r.repository_name),r.repository_role,r.default_branch,r.enabled,l.created_at
  FROM echo_service_repositories l
  JOIN echo_services s ON s.id=l.service_id
  JOIN echo_repositories r ON r.id=l.repository_id`

func scanServiceRepository(row scanner, item *domain.ServiceRepository) error {
	return row.Scan(&item.ID, &item.ProjectID, &item.ServiceID, &item.ServiceKey, &item.ServiceName, &item.RepositoryID, &item.Repository, &item.RepositoryRole, &item.DefaultBranch, &item.RepositoryEnabled, &item.CreatedAt)
}
