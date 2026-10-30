package store

import (
	"context"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

func (s *Store) ListIncidentAttachments(ctx context.Context, incidentID string) ([]domain.IncidentAttachment, error) {
	return s.listIncidentAttachments(ctx, incidentID, false)
}

func (s *Store) LoadIncidentAttachments(ctx context.Context, incidentID string) ([]domain.IncidentAttachment, error) {
	return s.listIncidentAttachments(ctx, incidentID, true)
}

func (s *Store) listIncidentAttachments(ctx context.Context, incidentID string, includeData bool) ([]domain.IncidentAttachment, error) {
	dataColumn := "NULL"
	if includeData {
		dataColumn = "data_blob"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,incident_id,whatsapp_message_id,media_kind,mime_type,file_name,size_bytes,sha256,`+dataColumn+`,created_at
      FROM echo_incident_attachments WHERE incident_id=? ORDER BY created_at,id LIMIT 4`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attachments []domain.IncidentAttachment
	for rows.Next() {
		var attachment domain.IncidentAttachment
		var data []byte
		if err := rows.Scan(&attachment.ID, &attachment.IncidentID, &attachment.WhatsAppMessageID, &attachment.Kind, &attachment.MIMEType, &attachment.FileName, &attachment.SizeBytes, &attachment.SHA256, &data, &attachment.CreatedAt); err != nil {
			return nil, err
		}
		if includeData {
			attachment.Data = data
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

func (s *Store) PruneIncidentAttachments(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM echo_incident_attachments WHERE created_at < ?`, before.UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
