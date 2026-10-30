CREATE TABLE IF NOT EXISTS echo_repository_workflow_alerts (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  repository_id CHAR(29) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  alert_status ENUM('open','resolved') NOT NULL,
  summary VARCHAR(300) NOT NULL,
  head_sha VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  failed_workflows_json JSON NOT NULL,
  opened_at DATETIME(6) NOT NULL,
  last_seen_at DATETIME(6) NOT NULL,
  resolved_at DATETIME(6) NULL,
  UNIQUE KEY uq_repository_workflow_alert (repository_id),
  KEY idx_repository_workflow_alert_status (alert_status, last_seen_at),
  CONSTRAINT fk_repository_workflow_alert_repository FOREIGN KEY (repository_id) REFERENCES echo_repositories(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
