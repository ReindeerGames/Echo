CREATE TABLE IF NOT EXISTS echo_briefing_settings (
  singleton_id TINYINT UNSIGNED NOT NULL PRIMARY KEY,
  enabled BOOLEAN NOT NULL DEFAULT FALSE,
  local_time CHAR(5) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '08:00',
  timezone VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'UTC',
  last_sent_local_date CHAR(10) CHARACTER SET ascii COLLATE ascii_bin NULL,
  last_sent_at DATETIME(6) NULL,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT chk_echo_briefing_singleton CHECK (singleton_id = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
INSERT IGNORE INTO echo_briefing_settings(singleton_id) VALUES(1);
