package store

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

const maxServiceRunbooks = 20

var ErrInvalidRunbook = errors.New("invalid service runbook")
var ErrRunbookLimit = errors.New("service runbook limit reached")
var ErrRunbookServiceMismatch = errors.New("service runbook cannot move between services")

func ValidateServiceRunbook(item domain.ServiceRunbook) error {
	if len(strings.TrimSpace(item.ServiceID)) != 28 {
		return ErrInvalidRunbook
	}
	if item.ID != "" && len(strings.TrimSpace(item.ID)) != 28 {
		return ErrInvalidRunbook
	}
	title := strings.TrimSpace(item.Title)
	guidance := strings.TrimSpace(item.Guidance)
	if utf8.RuneCountInString(title) < 3 || utf8.RuneCountInString(title) > 120 || utf8.RuneCountInString(guidance) < 3 || utf8.RuneCountInString(guidance) > 4000 {
		return ErrInvalidRunbook
	}
	reference := strings.TrimSpace(item.ReferenceURL)
	if reference == "" {
		return nil
	}
	if len(reference) > 1000 || strings.ContainsAny(reference, "\r\n\x00") {
		return ErrInvalidRunbook
	}
	parsed, err := url.Parse(reference)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return ErrInvalidRunbook
	}
	return nil
}

func normalizeServiceRunbook(item domain.ServiceRunbook) domain.ServiceRunbook {
	item.ID = strings.TrimSpace(item.ID)
	item.ServiceID = strings.TrimSpace(item.ServiceID)
	item.Title = strings.Join(strings.Fields(item.Title), " ")
	item.Guidance = strings.TrimSpace(strings.ReplaceAll(item.Guidance, "\r\n", "\n"))
	item.ReferenceURL = strings.TrimSpace(item.ReferenceURL)
	return item
}

func (s *Store) ListServiceRunbooks(ctx context.Context, serviceID string, limit int) ([]domain.ServiceRunbook, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	query := serviceRunbookSelect
	args := []any{}
	if serviceID = strings.TrimSpace(serviceID); serviceID != "" {
		query += ` WHERE r.service_id=?`
		args = append(args, serviceID)
	}
	query += ` ORDER BY s.name,r.title,r.id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ServiceRunbook, 0, min(limit, 200))
	for rows.Next() {
		var item domain.ServiceRunbook
		if err := scanServiceRunbook(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SaveServiceRunbook(ctx context.Context, item domain.ServiceRunbook) (domain.ServiceRunbook, error) {
	item = normalizeServiceRunbook(item)
	if err := ValidateServiceRunbook(item); err != nil {
		return domain.ServiceRunbook{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ServiceRunbook{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if item.ID == "" {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM echo_services WHERE id=? FOR UPDATE`, item.ServiceID).Scan(&exists); err != nil {
			return domain.ServiceRunbook{}, notFound(err)
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_service_runbooks WHERE service_id=?`, item.ServiceID).Scan(&count); err != nil {
			return domain.ServiceRunbook{}, err
		}
		if count >= maxServiceRunbooks {
			return domain.ServiceRunbook{}, ErrRunbookLimit
		}
		item.ID = id.New("rbk")
		if _, err := tx.ExecContext(ctx, `INSERT INTO echo_service_runbooks(id,service_id,title,guidance,reference_url) VALUES(?,?,?,?,?)`, item.ID, item.ServiceID, item.Title, item.Guidance, item.ReferenceURL); err != nil {
			return domain.ServiceRunbook{}, err
		}
	} else {
		var serviceID string
		if err := tx.QueryRowContext(ctx, `SELECT service_id FROM echo_service_runbooks WHERE id=? FOR UPDATE`, item.ID).Scan(&serviceID); err != nil {
			return domain.ServiceRunbook{}, notFound(err)
		}
		if serviceID != item.ServiceID {
			return domain.ServiceRunbook{}, ErrRunbookServiceMismatch
		}
		if _, err := tx.ExecContext(ctx, `UPDATE echo_service_runbooks SET title=?,guidance=?,reference_url=? WHERE id=?`, item.Title, item.Guidance, item.ReferenceURL, item.ID); err != nil {
			return domain.ServiceRunbook{}, err
		}
	}
	if err := scanServiceRunbook(tx.QueryRowContext(ctx, serviceRunbookSelect+` WHERE r.id=?`, item.ID), &item); err != nil {
		return domain.ServiceRunbook{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ServiceRunbook{}, err
	}
	return item, nil
}

func (s *Store) DeleteServiceRunbook(ctx context.Context, runbookID string) (domain.ServiceRunbook, error) {
	if len(strings.TrimSpace(runbookID)) != 28 {
		return domain.ServiceRunbook{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ServiceRunbook{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var item domain.ServiceRunbook
	if err := scanServiceRunbook(tx.QueryRowContext(ctx, serviceRunbookSelect+` WHERE r.id=? FOR UPDATE`, runbookID), &item); err != nil {
		return domain.ServiceRunbook{}, notFound(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM echo_service_runbooks WHERE id=?`, item.ID); err != nil {
		return domain.ServiceRunbook{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ServiceRunbook{}, err
	}
	return item, nil
}

const serviceRunbookSelect = `SELECT r.id,s.project_id,r.service_id,s.name,r.title,r.guidance,r.reference_url,r.created_at,r.updated_at
  FROM echo_service_runbooks r
  JOIN echo_services s ON s.id=r.service_id`

func scanServiceRunbook(row scanner, item *domain.ServiceRunbook) error {
	return row.Scan(&item.ID, &item.ProjectID, &item.ServiceID, &item.ServiceName, &item.Title, &item.Guidance, &item.ReferenceURL, &item.CreatedAt, &item.UpdatedAt)
}
