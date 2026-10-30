CREATE TABLE IF NOT EXISTS echo_incident_attachments (
  id CHAR(29) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  incident_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  whatsapp_message_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  media_kind ENUM('image') NOT NULL,
  mime_type VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  file_name VARCHAR(255) NOT NULL DEFAULT '',
  size_bytes BIGINT UNSIGNED NOT NULL,
  sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  data_blob MEDIUMBLOB NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_incident_attachment_message (incident_id, whatsapp_message_id),
  KEY idx_echo_incident_attachment_created (created_at),
  CONSTRAINT fk_echo_attachment_incident FOREIGN KEY (incident_id) REFERENCES echo_incidents(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
