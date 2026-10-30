ALTER TABLE echo_deployments
  ADD COLUMN repository_full_name VARCHAR(400) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' AFTER workflow_url,
  ADD COLUMN workflow_name VARCHAR(255) NOT NULL DEFAULT '' AFTER repository_full_name,
  ADD COLUMN external_run_id BIGINT NULL AFTER workflow_name,
  ADD KEY idx_echo_deployment_match (artifact_ref, repository_full_name, workflow_name, status);
