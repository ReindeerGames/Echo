package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

func insertIncidentAttachments(ctx context.Context, tx *sql.Tx, incidentID, messageID string, attachments []domain.IncidentAttachment) error {
	for _, attachment := range attachments {
		if err := validateIncidentAttachment(attachment); err != nil {
			return err
		}
		if attachment.ID == "" {
			attachment.ID = id.New("att")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO echo_incident_attachments(id,incident_id,whatsapp_message_id,media_kind,mime_type,file_name,size_bytes,sha256,data_blob) VALUES(?,?,?,?,?,?,?,?,?)`, attachment.ID, incidentID, messageID, attachment.Kind, attachment.MIMEType, attachment.FileName, attachment.SizeBytes, strings.ToLower(attachment.SHA256), attachment.Data); err != nil {
			return err
		}
	}
	return nil
}

func validateIncidentAttachment(attachment domain.IncidentAttachment) error {
	_, hashErr := hex.DecodeString(attachment.SHA256)
	digest := sha256.Sum256(attachment.Data)
	detectedMIME := strings.ToLower(strings.TrimSpace(http.DetectContentType(attachment.Data)))
	mimeAllowed := attachment.MIMEType == "image/jpeg" || attachment.MIMEType == "image/png" || attachment.MIMEType == "image/webp"
	if attachment.Kind != "image" || !mimeAllowed || detectedMIME != attachment.MIMEType || !utf8.ValidString(attachment.FileName) || utf8.RuneCountInString(attachment.FileName) > 255 || len(attachment.Data) == 0 || int64(len(attachment.Data)) != attachment.SizeBytes || attachment.SizeBytes > 5<<20 || len(attachment.SHA256) != 64 || hashErr != nil || !strings.EqualFold(hex.EncodeToString(digest[:]), attachment.SHA256) {
		return errors.New("invalid incident attachment")
	}
	return nil
}

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
      FROM echo_incident_attachments WHERE incident_id=? ORDER BY created_at DESC,id DESC LIMIT 4`, incidentID)
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
