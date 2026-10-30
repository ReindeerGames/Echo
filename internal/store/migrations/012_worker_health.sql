CREATE TABLE IF NOT EXISTS echo_worker_heartbeats (
  worker_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  started_at DATETIME(6) NOT NULL,
  last_seen_at DATETIME(6) NOT NULL,
  KEY idx_echo_worker_heartbeat_seen (last_seen_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_worker_health_alerts (
  alert_key VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  alert_status ENUM('open','resolved') NOT NULL,
  ready_jobs INT UNSIGNED NOT NULL DEFAULT 0,
  oldest_ready_at DATETIME(6) NULL,
  worker_last_seen_at DATETIME(6) NULL,
  summary VARCHAR(1000) NOT NULL,
  opened_at DATETIME(6) NULL,
  last_seen_at DATETIME(6) NULL,
  resolved_at DATETIME(6) NULL,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  KEY idx_echo_worker_alert_status (alert_status, last_seen_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
