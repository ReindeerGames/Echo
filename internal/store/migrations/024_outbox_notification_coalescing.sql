ALTER TABLE echo_whatsapp_outbox
  ADD COLUMN coalesce_key VARCHAR(120) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER body,
  ADD COLUMN coalesce_count INT UNSIGNED NOT NULL DEFAULT 1 AFTER coalesce_key,
  ADD UNIQUE KEY uq_echo_outbox_coalesce (chat_jid, coalesce_key);
