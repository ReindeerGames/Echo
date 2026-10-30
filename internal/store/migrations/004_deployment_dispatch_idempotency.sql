ALTER TABLE echo_deployments
  ADD COLUMN approval_id CHAR(29) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER incident_id,
  MODIFY COLUMN status ENUM('dispatching','queued','running','verified','failed','rolled_back','cancelled') NOT NULL DEFAULT 'dispatching',
  ADD UNIQUE KEY uq_echo_deployment_approval (approval_id),
  ADD CONSTRAINT fk_echo_deployment_approval FOREIGN KEY (approval_id) REFERENCES echo_approvals(id) ON DELETE RESTRICT;
