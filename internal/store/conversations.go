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

func (s *Store) BeginConversationTurn(ctx context.Context, channel, participantKey, externalMessageID, body string) (domain.Conversation, domain.ConversationMessage, bool, error) {
	channel = strings.TrimSpace(channel)
	participantKey = strings.TrimSpace(participantKey)
	externalMessageID = strings.TrimSpace(externalMessageID)
	body = strings.TrimSpace(body)
	if channel != "whatsapp" || participantKey == "" || externalMessageID == "" || body == "" {
		return domain.Conversation{}, domain.ConversationMessage{}, false, errors.New("channel, participant, message ID and body are required")
	}
	if len(participantKey) > 255 || len(externalMessageID) > 255 || len(body) > 16000 {
		return domain.Conversation{}, domain.ConversationMessage{}, false, errors.New("conversation turn exceeds storage limits")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Conversation{}, domain.ConversationMessage{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	conversationID := id.New("cnv")
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO echo_conversations(id,channel,participant_key) VALUES(?,?,?)`, conversationID, channel, participantKey); err != nil {
		return domain.Conversation{}, domain.ConversationMessage{}, false, err
	}
	var conversation domain.Conversation
	if err := tx.QueryRowContext(ctx, `SELECT id,channel,participant_key,created_at,updated_at FROM echo_conversations WHERE channel=? AND participant_key=? FOR UPDATE`, channel, participantKey).Scan(&conversation.ID, &conversation.Channel, &conversation.ParticipantKey, &conversation.CreatedAt, &conversation.UpdatedAt); err != nil {
		return domain.Conversation{}, domain.ConversationMessage{}, false, err
	}
	messageID := id.New("msg")
	result, err := tx.ExecContext(ctx, `INSERT IGNORE INTO echo_conversation_messages(id,conversation_id,message_role,external_message_id,body) VALUES(?,?,'user',?,?)`, messageID, conversation.ID, externalMessageID, body)
	if err != nil {
		return domain.Conversation{}, domain.ConversationMessage{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Conversation{}, domain.ConversationMessage{}, false, err
	}
	var message domain.ConversationMessage
	if err := tx.QueryRowContext(ctx, `SELECT id,conversation_id,message_role,COALESCE(external_message_id,''),COALESCE(reply_to_message_id,''),body,intent_name,created_at FROM echo_conversation_messages WHERE conversation_id=? AND external_message_id=?`, conversation.ID, externalMessageID).Scan(&message.ID, &message.ConversationID, &message.Role, &message.ExternalMessageID, &message.ReplyToMessageID, &message.Body, &message.Intent, &message.CreatedAt); err != nil {
		return domain.Conversation{}, domain.ConversationMessage{}, false, err
	}
	if affected == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE echo_conversations SET updated_at=NOW(6) WHERE id=?`, conversation.ID); err != nil {
			return domain.Conversation{}, domain.ConversationMessage{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Conversation{}, domain.ConversationMessage{}, false, err
	}
	return conversation, message, affected == 1, nil
}

func (s *Store) ListConversationMessages(ctx context.Context, conversationID string, limit int) ([]domain.ConversationMessage, error) {
	if limit < 1 || limit > 50 {
		limit = 12
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,message_role,COALESCE(external_message_id,''),COALESCE(reply_to_message_id,''),body,intent_name,created_at FROM (
      SELECT id,conversation_id,message_role,external_message_id,reply_to_message_id,body,intent_name,created_at
      FROM echo_conversation_messages WHERE conversation_id=? ORDER BY created_at DESC,id DESC LIMIT ?
    ) recent ORDER BY created_at ASC,id ASC`, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []domain.ConversationMessage
	for rows.Next() {
		var message domain.ConversationMessage
		if err := rows.Scan(&message.ID, &message.ConversationID, &message.Role, &message.ExternalMessageID, &message.ReplyToMessageID, &message.Body, &message.Intent, &message.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) CompleteConversationTurn(ctx context.Context, conversationID, userMessageID, answer, intent string, runs []domain.ToolRun, replyChatJID ...string) (domain.ConversationMessage, error) {
	answer = strings.TrimSpace(answer)
	intent = strings.TrimSpace(intent)
	if conversationID == "" || userMessageID == "" || answer == "" || len(answer) > 16000 || len(intent) > 80 {
		return domain.ConversationMessage{}, errors.New("invalid conversation reply")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConversationMessage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	messageID := id.New("msg")
	insert, err := tx.ExecContext(ctx, `INSERT IGNORE INTO echo_conversation_messages(id,conversation_id,message_role,reply_to_message_id,body,intent_name) VALUES(?,?,'assistant',?,?,?)`, messageID, conversationID, userMessageID, answer, intent)
	if err != nil {
		return domain.ConversationMessage{}, err
	}
	created, err := insert.RowsAffected()
	if err != nil {
		return domain.ConversationMessage{}, err
	}
	var message domain.ConversationMessage
	if err := tx.QueryRowContext(ctx, `SELECT id,conversation_id,message_role,COALESCE(external_message_id,''),COALESCE(reply_to_message_id,''),body,intent_name,created_at FROM echo_conversation_messages WHERE reply_to_message_id=?`, userMessageID).Scan(&message.ID, &message.ConversationID, &message.Role, &message.ExternalMessageID, &message.ReplyToMessageID, &message.Body, &message.Intent, &message.CreatedAt); err != nil {
		return domain.ConversationMessage{}, err
	}
	for _, run := range runs {
		if run.ID == "" {
			run.ID = id.New("tol")
		}
		if len(run.Arguments) == 0 {
			run.Arguments = json.RawMessage(`{}`)
		}
		if run.Status != "succeeded" && run.Status != "failed" {
			return domain.ConversationMessage{}, fmt.Errorf("invalid tool status %q", run.Status)
		}
		if !json.Valid(run.Arguments) || run.CallID == "" || run.Name == "" || len(run.CallID) > 255 || len(run.Name) > 120 || len(run.Error) > 1000 {
			return domain.ConversationMessage{}, errors.New("invalid tool run")
		}
		if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO echo_tool_runs(id,conversation_id,message_id,call_id,tool_name,arguments_json,output_text,run_status,error_text,duration_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`, run.ID, conversationID, userMessageID, run.CallID, run.Name, run.Arguments, run.Output, run.Status, run.Error, run.DurationMS); err != nil {
			return domain.ConversationMessage{}, err
		}
	}
	if created == 1 && len(replyChatJID) > 0 && strings.TrimSpace(replyChatJID[0]) != "" {
		chatJID := strings.TrimSpace(replyChatJID[0])
		if len(chatJID) > 255 {
			return domain.ConversationMessage{}, errors.New("reply chat JID exceeds storage limits")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO echo_whatsapp_outbox(id,chat_jid,body) VALUES(?,?,?)`, id.New("out"), chatJID, answer); err != nil {
			return domain.ConversationMessage{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE echo_conversations SET updated_at=NOW(6) WHERE id=?`, conversationID); err != nil {
		return domain.ConversationMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ConversationMessage{}, err
	}
	return message, nil
}

func (s *Store) ConversationReply(ctx context.Context, userMessageID string) (domain.ConversationMessage, error) {
	var message domain.ConversationMessage
	err := s.db.QueryRowContext(ctx, `SELECT id,conversation_id,message_role,COALESCE(external_message_id,''),COALESCE(reply_to_message_id,''),body,intent_name,created_at FROM echo_conversation_messages WHERE reply_to_message_id=?`, userMessageID).Scan(&message.ID, &message.ConversationID, &message.Role, &message.ExternalMessageID, &message.ReplyToMessageID, &message.Body, &message.Intent, &message.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationMessage{}, ErrNotFound
	}
	return message, err
}

func (s *Store) ListConversations(ctx context.Context, limit int) ([]domain.Conversation, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,channel,participant_key,created_at,updated_at FROM echo_conversations ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conversations []domain.Conversation
	for rows.Next() {
		var conversation domain.Conversation
		if err := rows.Scan(&conversation.ID, &conversation.Channel, &conversation.ParticipantKey, &conversation.CreatedAt, &conversation.UpdatedAt); err != nil {
			return nil, err
		}
		conversations = append(conversations, conversation)
	}
	return conversations, rows.Err()
}

func (s *Store) GetConversation(ctx context.Context, conversationID string) (domain.Conversation, error) {
	var conversation domain.Conversation
	err := s.db.QueryRowContext(ctx, `SELECT id,channel,participant_key,created_at,updated_at FROM echo_conversations WHERE id=?`, conversationID).Scan(&conversation.ID, &conversation.Channel, &conversation.ParticipantKey, &conversation.CreatedAt, &conversation.UpdatedAt)
	return conversation, notFound(err)
}

func (s *Store) ListToolRuns(ctx context.Context, conversationID string, limit int) ([]domain.ToolRun, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,message_id,call_id,tool_name,arguments_json,output_text,run_status,error_text,duration_ms,created_at FROM echo_tool_runs WHERE conversation_id=? ORDER BY created_at DESC LIMIT ?`, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []domain.ToolRun
	for rows.Next() {
		var run domain.ToolRun
		if err := rows.Scan(&run.ID, &run.ConversationID, &run.MessageID, &run.CallID, &run.Name, &run.Arguments, &run.Output, &run.Status, &run.Error, &run.DurationMS, &run.CreatedAt); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) PruneConversationHistory(ctx context.Context, before time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM echo_conversation_messages WHERE created_at<?`, before.UTC())
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE c FROM echo_conversations c LEFT JOIN echo_conversation_messages m ON m.conversation_id=c.id WHERE m.id IS NULL AND c.updated_at<?`, before.UTC()); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
