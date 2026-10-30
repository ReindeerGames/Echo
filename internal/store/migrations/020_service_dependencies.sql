CREATE TABLE IF NOT EXISTS echo_service_dependencies (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  service_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  depends_on_service_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  dependency_kind ENUM('runtime','data','messaging','network','other') NOT NULL DEFAULT 'runtime',
  description VARCHAR(300) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_service_dependency (service_id, depends_on_service_id),
  KEY idx_dependency_target (depends_on_service_id, service_id),
  CONSTRAINT fk_dependency_service FOREIGN KEY (service_id) REFERENCES echo_services(id) ON DELETE CASCADE,
  CONSTRAINT fk_dependency_target FOREIGN KEY (depends_on_service_id) REFERENCES echo_services(id) ON DELETE CASCADE,
  CONSTRAINT chk_dependency_not_self CHECK (service_id <> depends_on_service_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
