CREATE TABLE IF NOT EXISTS echo_service_repositories (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  service_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  repository_id CHAR(29) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_service_repository (service_id, repository_id),
  KEY idx_service_repository_repository (repository_id, service_id),
  CONSTRAINT fk_service_repository_service FOREIGN KEY (service_id) REFERENCES echo_services(id) ON DELETE CASCADE,
  CONSTRAINT fk_service_repository_repository FOREIGN KEY (repository_id) REFERENCES echo_repositories(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
