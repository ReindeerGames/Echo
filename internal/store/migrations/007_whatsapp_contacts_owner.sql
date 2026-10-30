CREATE TABLE IF NOT EXISTS echo_whatsapp_contacts (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  primary_jid VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  alternate_jid VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  display_name VARCHAR(255) NOT NULL DEFAULT '',
  message_count BIGINT UNSIGNED NOT NULL DEFAULT 1,
  first_seen_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  last_seen_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_contact_primary_jid (primary_jid),
  UNIQUE KEY uq_echo_contact_alternate_jid (alternate_jid),
  KEY idx_echo_contact_last_seen (last_seen_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_owner_settings (
  singleton_id TINYINT UNSIGNED NOT NULL PRIMARY KEY,
  contact_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  set_by VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT chk_echo_owner_singleton CHECK (singleton_id = 1),
  CONSTRAINT fk_echo_owner_contact FOREIGN KEY (contact_id) REFERENCES echo_whatsapp_contacts(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
