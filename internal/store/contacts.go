package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/id"
)

func (s *Store) UpsertWhatsAppContact(ctx context.Context, msg domain.InboundMessage) (domain.WhatsAppContact, bool, error) {
	primary, alternate := contactJIDs(msg)
	if primary == "" {
		return domain.WhatsAppContact{}, false, errors.New("direct message has no usable sender identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.WhatsAppContact{}, false, err
	}
	defer tx.Rollback()

	var contactID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM echo_whatsapp_contacts
      WHERE primary_jid IN (?,?) OR alternate_jid IN (?,?) LIMIT 1 FOR UPDATE`, primary, alternate, primary, alternate).Scan(&contactID)
	created := false
	if errors.Is(err, sql.ErrNoRows) {
		contactID = id.New("ctc")
		var alternateValue any
		if alternate != "" {
			alternateValue = alternate
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO echo_whatsapp_contacts
          (id,primary_jid,alternate_jid,display_name,message_count,first_seen_at,last_seen_at)
          VALUES(?,?,?,?,1,?,?)`, contactID, primary, alternateValue, strings.TrimSpace(msg.SenderName), msg.Timestamp, msg.Timestamp)
		created = err == nil
	} else if err == nil {
		var alternateValue any
		if alternate != "" {
			alternateValue = alternate
		}
		_, err = tx.ExecContext(ctx, `UPDATE echo_whatsapp_contacts SET
          primary_jid=?,alternate_jid=?,display_name=IF(?='',display_name,?),
          message_count=message_count+1,last_seen_at=GREATEST(last_seen_at,?) WHERE id=?`,
			primary, alternateValue, strings.TrimSpace(msg.SenderName), strings.TrimSpace(msg.SenderName), msg.Timestamp, contactID)
	}
	if err != nil {
		return domain.WhatsAppContact{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.WhatsAppContact{}, false, err
	}
	contact, err := s.GetWhatsAppContact(ctx, contactID)
	return contact, created, err
}

func (s *Store) ListWhatsAppContacts(ctx context.Context) ([]domain.WhatsAppContact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,primary_jid,COALESCE(alternate_jid,''),display_name,message_count,first_seen_at,last_seen_at
      FROM echo_whatsapp_contacts ORDER BY last_seen_at DESC LIMIT 250`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contacts []domain.WhatsAppContact
	for rows.Next() {
		var contact domain.WhatsAppContact
		if err := rows.Scan(&contact.ID, &contact.JID, &contact.AlternateJID, &contact.DisplayName, &contact.MessageCount, &contact.FirstSeenAt, &contact.LastSeenAt); err != nil {
			return nil, err
		}
		contacts = append(contacts, contact)
	}
	return contacts, rows.Err()
}

func (s *Store) GetWhatsAppContact(ctx context.Context, contactID string) (domain.WhatsAppContact, error) {
	var contact domain.WhatsAppContact
	err := s.db.QueryRowContext(ctx, `SELECT id,primary_jid,COALESCE(alternate_jid,''),display_name,message_count,first_seen_at,last_seen_at
      FROM echo_whatsapp_contacts WHERE id=?`, contactID).Scan(&contact.ID, &contact.JID, &contact.AlternateJID, &contact.DisplayName, &contact.MessageCount, &contact.FirstSeenAt, &contact.LastSeenAt)
	return contact, notFound(err)
}

func (s *Store) SetOwnerContact(ctx context.Context, contactID, actor string) (domain.WhatsAppContact, time.Time, error) {
	contact, err := s.GetWhatsAppContact(ctx, contactID)
	if err != nil {
		return domain.WhatsAppContact{}, time.Time{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO echo_owner_settings(singleton_id,contact_id,set_by) VALUES(1,?,?)
      ON DUPLICATE KEY UPDATE contact_id=VALUES(contact_id),set_by=VALUES(set_by)`, contactID, actor)
	if err != nil {
		return domain.WhatsAppContact{}, time.Time{}, err
	}
	var updated time.Time
	err = s.db.QueryRowContext(ctx, `SELECT updated_at FROM echo_owner_settings WHERE singleton_id=1`).Scan(&updated)
	return contact, updated, err
}

func (s *Store) GetOwnerContact(ctx context.Context) (domain.WhatsAppContact, time.Time, error) {
	var contact domain.WhatsAppContact
	var updated time.Time
	err := s.db.QueryRowContext(ctx, `SELECT c.id,c.primary_jid,COALESCE(c.alternate_jid,''),c.display_name,c.message_count,c.first_seen_at,c.last_seen_at,o.updated_at
      FROM echo_owner_settings o JOIN echo_whatsapp_contacts c ON c.id=o.contact_id WHERE o.singleton_id=1`).Scan(
		&contact.ID, &contact.JID, &contact.AlternateJID, &contact.DisplayName, &contact.MessageCount, &contact.FirstSeenAt, &contact.LastSeenAt, &updated)
	return contact, updated, notFound(err)
}

func (s *Store) ClearOwnerContact(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM echo_owner_settings WHERE singleton_id=1`)
	return err
}

func contactJIDs(msg domain.InboundMessage) (string, string) {
	candidates := []string{msg.SenderJID, msg.SenderAltJID, msg.ChatJID}
	primary := ""
	for _, candidate := range candidates {
		normalized := normalizeContactJID(candidate)
		if strings.HasSuffix(normalized, "@s.whatsapp.net") {
			primary = normalized
			break
		}
	}
	if primary == "" {
		for _, candidate := range candidates {
			if normalized := normalizeContactJID(candidate); normalized != "" {
				primary = normalized
				break
			}
		}
	}
	alternate := ""
	for _, candidate := range candidates {
		normalized := normalizeContactJID(candidate)
		if normalized != "" && normalized != primary {
			alternate = normalized
			break
		}
	}
	return primary, alternate
}

func normalizeContactJID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	at := strings.LastIndexByte(value, '@')
	if at <= 0 || at == len(value)-1 {
		return ""
	}
	user := value[:at]
	if colon := strings.IndexByte(user, ':'); colon >= 0 {
		user = user[:colon]
	}
	if user == "" {
		return ""
	}
	return user + value[at:]
}
