package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

var ErrIncidentNotOpen = errors.New("incident is not open for follow-up evidence")

func (s *Store) FindIncidentByMessageReference(ctx context.Context, chatJID, messageID string) (domain.Incident, error) {
	var incidentID string
	err := s.db.QueryRowContext(ctx, `SELECT incident_id FROM echo_incident_messages WHERE chat_jid=? AND whatsapp_message_id=?`, strings.TrimSpace(chatJID), strings.TrimSpace(messageID)).Scan(&incidentID)
	if err != nil {
		return domain.Incident{}, notFound(err)
	}
	return s.GetIncident(ctx, incidentID)
}

func (s *Store) ListIncidentMessages(ctx context.Context, incidentID string, limit int) ([]domain.IncidentMessage, error) {
	if limit < 1 || limit > 50 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,incident_id,whatsapp_message_id,sender_name,direction,body,metadata_json,message_timestamp,created_at
      FROM (SELECT id,incident_id,whatsapp_message_id,sender_name,direction,body,metadata_json,message_timestamp,created_at
            FROM echo_incident_messages WHERE incident_id=? ORDER BY created_at DESC,id DESC LIMIT ?) recent
      ORDER BY created_at,id`, incidentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]domain.IncidentMessage, 0)
	for rows.Next() {
		var message domain.IncidentMessage
		if err := rows.Scan(&message.ID, &message.IncidentID, &message.WhatsAppMessageID, &message.SenderName, &message.Direction, &message.Body, &message.Metadata, &message.MessageTimestamp, &message.CreatedAt); err != nil {
			return nil, err
		}
		var metadata struct {
			ReplyToMessageID string `json:"replyToMessageId"`
		}
		_ = json.Unmarshal(message.Metadata, &metadata)
		message.ReplyToMessageID = metadata.ReplyToMessageID
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) AppendIncidentFollowUp(ctx context.Context, incidentID string, msg domain.InboundMessage) (domain.Incident, bool, error) {
	if strings.TrimSpace(incidentID) == "" || strings.TrimSpace(msg.MessageID) == "" || strings.TrimSpace(msg.ChatJID) == "" || strings.TrimSpace(msg.ReplyToMessageID) == "" || (strings.TrimSpace(msg.Body) == "" && len(msg.Attachments) == 0) {
		return domain.Incident{}, false, errors.New("invalid incident follow-up")
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Incident{}, false, err
	}
	defer tx.Rollback()
	var existingIncidentID string
	err = tx.QueryRowContext(ctx, `SELECT incident_id FROM echo_incident_messages WHERE chat_jid=? AND whatsapp_message_id=?`, msg.ChatJID, msg.MessageID).Scan(&existingIncidentID)
	if err == nil {
		if err = tx.Commit(); err != nil {
			return domain.Incident{}, false, err
		}
		item, getErr := s.GetIncident(ctx, existingIncidentID)
		return item, false, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Incident{}, false, err
	}
	var groupJID, status string
	var revision uint64
	if err = tx.QueryRowContext(ctx, `SELECT group_jid,status,evidence_revision FROM echo_incidents WHERE id=? FOR UPDATE`, incidentID).Scan(&groupJID, &status, &revision); err != nil {
		return domain.Incident{}, false, notFound(err)
	}
	if groupJID != msg.ChatJID {
		return domain.Incident{}, false, errors.New("follow-up group does not match incident")
	}
	if status == "resolved" || status == "closed" {
		return domain.Incident{}, false, ErrIncidentNotOpen
	}
	var referenceCount int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM echo_incident_messages WHERE incident_id=? AND chat_jid=? AND whatsapp_message_id=?`, incidentID, msg.ChatJID, msg.ReplyToMessageID).Scan(&referenceCount); err != nil {
		return domain.Incident{}, false, err
	}
	if referenceCount != 1 {
		return domain.Incident{}, false, errors.New("follow-up reference does not belong to incident")
	}
	metadata, _ := json.Marshal(map[string]any{"mentionedJids": msg.MentionedJIDs, "isGroup": msg.IsGroup, "replyToMessageId": msg.ReplyToMessageID, "attachmentCount": len(msg.Attachments), "followUp": true})
	if _, err = tx.ExecContext(ctx, `INSERT INTO echo_incident_messages(id,incident_id,whatsapp_message_id,chat_jid,sender_jid,sender_name,direction,body,metadata_json,message_timestamp) VALUES(?,?,?,?,?,?,'inbound',?,?,?)`, id.New("msg"), incidentID, msg.MessageID, msg.ChatJID, msg.SenderJID, msg.SenderName, msg.Body, metadata, msg.Timestamp); err != nil {
		return domain.Incident{}, false, err
	}
	if err = insertIncidentAttachments(ctx, tx, incidentID, msg.MessageID, msg.Attachments); err != nil {
		return domain.Incident{}, false, err
	}
	revision++
	if _, err = tx.ExecContext(ctx, `UPDATE echo_incidents SET evidence_revision=?,status=IF(status IN ('reported','triaging','awaiting_owner'),'triaging',status) WHERE id=?`, revision, incidentID); err != nil {
		return domain.Incident{}, false, err
	}
	invalidated, err := tx.ExecContext(ctx, `UPDATE echo_approvals SET status='expired',resolved_by='system:evidence_changed',resolved_at=NOW(6) WHERE incident_id=? AND status='pending'`, incidentID)
	if err != nil {
		return domain.Incident{}, false, err
	}
	invalidatedApprovals, err := invalidated.RowsAffected()
	if err != nil {
		return domain.Incident{}, false, err
	}
	jobPayload, _ := json.Marshal(map[string]any{"incidentId": incidentID, "evidenceRevision": revision})
	if _, err = tx.ExecContext(ctx, `INSERT INTO echo_jobs(id,job_type,payload_json) VALUES(?,'analyze_incident',?)`, id.New("job"), jobPayload); err != nil {
		return domain.Incident{}, false, err
	}
	acknowledgement := fmt.Sprintf("Added this evidence to Echo incident %s and queued an updated analysis.", incidentID)
	if invalidatedApprovals > 0 {
		acknowledgement += fmt.Sprintf(" %d pending approval(s) based on older evidence expired safely.", invalidatedApprovals)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO echo_whatsapp_outbox(id,chat_jid,body) VALUES(?,?,?)`, id.New("out"), msg.ChatJID, acknowledgement); err != nil {
		return domain.Incident{}, false, err
	}
	if err = insertAudit(ctx, tx, "whatsapp", msg.SenderJID, "incident.follow_up", "incident", incidentID, "succeeded", map[string]any{"groupJid": msg.ChatJID, "messageId": msg.MessageID, "replyToMessageId": msg.ReplyToMessageID, "attachmentCount": len(msg.Attachments), "evidenceRevision": revision, "invalidatedApprovals": invalidatedApprovals}); err != nil {
		return domain.Incident{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Incident{}, false, err
	}
	item, getErr := s.GetIncident(ctx, incidentID)
	return item, true, getErr
}
