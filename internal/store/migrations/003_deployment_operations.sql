ALTER TABLE echo_deployments
  ADD COLUMN operation_type ENUM('deploy','rollback') NOT NULL DEFAULT 'deploy' AFTER environment_name,
  ADD COLUMN source_deployment_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER artifact_ref,
  ADD KEY idx_echo_deployment_source (source_deployment_id),
  ADD CONSTRAINT fk_echo_deployment_source FOREIGN KEY (source_deployment_id) REFERENCES echo_deployments(id) ON DELETE RESTRICT;
