CREATE TABLE IF NOT EXISTS echo_runtime_component_alerts (
  component_name VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  alert_status ENUM('open','resolved') NOT NULL,
  component_status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  summary VARCHAR(300) NOT NULL,
  opened_at DATETIME(6) NULL,
  last_seen_at DATETIME(6) NULL,
  resolved_at DATETIME(6) NULL,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  KEY idx_echo_runtime_component_alert_status (alert_status, last_seen_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
