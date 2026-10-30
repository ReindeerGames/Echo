ALTER TABLE echo_whatsapp_outbox
  ADD COLUMN delivery_class ENUM('interactive','proactive') NOT NULL DEFAULT 'interactive' AFTER body,
  ADD COLUMN suppressed_at DATETIME(6) NULL AFTER sent_at,
  MODIFY COLUMN status ENUM('queued','sending','sent','failed','dead','suppressed') NOT NULL DEFAULT 'queued',
  ADD KEY idx_echo_outbox_class_claim (delivery_class,status,available_at,locked_until),
  ADD KEY idx_echo_outbox_suppressed_retention (status,suppressed_at,id);
-- echo:split
UPDATE echo_whatsapp_outbox SET delivery_class='proactive'
WHERE status IN ('queued','sending','failed','dead') AND (
  body LIKE 'Echo monitoring update · %' OR
  body LIKE 'Echo alert %' OR
  body LIKE 'Echo runtime alert %' OR
  body LIKE 'Echo worker queue %' OR
  body LIKE 'Echo GitHub workflow alert %'
);
-- echo:split
CREATE TABLE echo_notification_settings (
  singleton_id TINYINT UNSIGNED NOT NULL PRIMARY KEY,
  paused_until DATETIME(6) NULL,
  updated_by VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'system',
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT chk_echo_notification_settings_singleton CHECK (singleton_id = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
INSERT INTO echo_notification_settings(singleton_id) VALUES(1);
