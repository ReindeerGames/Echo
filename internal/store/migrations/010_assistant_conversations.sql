CREATE TABLE IF NOT EXISTS echo_conversations (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  channel ENUM('whatsapp') NOT NULL,
  participant_key VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_conversation_participant (channel, participant_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_conversation_messages (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  conversation_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  message_role ENUM('user','assistant') NOT NULL,
  external_message_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  reply_to_message_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NULL,
  body TEXT NOT NULL,
  intent_name VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_conversation_external (conversation_id, external_message_id),
  UNIQUE KEY uq_echo_conversation_reply (reply_to_message_id),
  KEY idx_echo_conversation_history (conversation_id, created_at),
  CONSTRAINT fk_echo_conversation_message FOREIGN KEY (conversation_id) REFERENCES echo_conversations(id) ON DELETE CASCADE,
  CONSTRAINT fk_echo_conversation_reply FOREIGN KEY (reply_to_message_id) REFERENCES echo_conversation_messages(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_tool_runs (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  conversation_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  message_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  call_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  tool_name VARCHAR(120) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  arguments_json JSON NOT NULL,
  output_text MEDIUMTEXT NOT NULL,
  run_status ENUM('succeeded','failed') NOT NULL,
  error_text VARCHAR(1000) NOT NULL DEFAULT '',
  duration_ms BIGINT NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_tool_call (conversation_id, call_id),
  KEY idx_echo_tool_message (message_id, created_at),
  CONSTRAINT fk_echo_tool_conversation FOREIGN KEY (conversation_id) REFERENCES echo_conversations(id) ON DELETE CASCADE,
  CONSTRAINT fk_echo_tool_message FOREIGN KEY (message_id) REFERENCES echo_conversation_messages(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
