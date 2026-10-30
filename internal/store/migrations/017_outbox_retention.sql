ALTER TABLE echo_whatsapp_outbox
  ADD KEY idx_echo_outbox_sent_retention (status, sent_at, id);
