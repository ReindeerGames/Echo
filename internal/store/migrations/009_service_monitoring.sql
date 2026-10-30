CREATE TABLE IF NOT EXISTS echo_services (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  project_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  service_key VARCHAR(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  name VARCHAR(180) NOT NULL,
  service_kind ENUM('application','worker','database','platform','external') NOT NULL DEFAULT 'application',
  criticality ENUM('low','medium','high','critical') NOT NULL DEFAULT 'medium',
  description TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  metadata_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_service_key (project_id, service_key),
  KEY idx_echo_service_project (project_id, enabled),
  CONSTRAINT fk_echo_service_project FOREIGN KEY (project_id) REFERENCES echo_projects(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_monitor_events (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  event_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  service_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_name VARCHAR(120) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  health_status ENUM('healthy','degraded','unhealthy','unknown') NOT NULL,
  severity ENUM('info','warning','critical') NOT NULL,
  summary VARCHAR(1000) NOT NULL,
  fingerprint VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  details_json JSON NOT NULL,
  observed_at DATETIME(6) NOT NULL,
  received_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_monitor_event (event_id),
  KEY idx_echo_monitor_service_time (service_id, observed_at),
  CONSTRAINT fk_echo_monitor_event_service FOREIGN KEY (service_id) REFERENCES echo_services(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_service_alerts (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  service_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  fingerprint VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  alert_status ENUM('open','resolved') NOT NULL DEFAULT 'open',
  severity ENUM('warning','critical') NOT NULL,
  title VARCHAR(300) NOT NULL,
  summary VARCHAR(1000) NOT NULL,
  latest_event_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  opened_at DATETIME(6) NOT NULL,
  last_seen_at DATETIME(6) NOT NULL,
  resolved_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_service_alert (service_id, fingerprint),
  KEY idx_echo_alert_status (alert_status, severity, last_seen_at),
  CONSTRAINT fk_echo_alert_service FOREIGN KEY (service_id) REFERENCES echo_services(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
