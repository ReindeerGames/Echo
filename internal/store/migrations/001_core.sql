-- Echo owns this schema. HyperMeow session tables belong in a separate database.
CREATE TABLE IF NOT EXISTS echo_schema_migrations (
  version VARCHAR(64) NOT NULL PRIMARY KEY,
  applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_projects (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  project_key VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  name VARCHAR(180) NOT NULL,
  description TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_projects_key (project_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_group_bindings (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  project_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  group_jid VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  group_name VARCHAR(255) NOT NULL,
  mention_required BOOLEAN NOT NULL DEFAULT TRUE,
  ack_template VARCHAR(500) NOT NULL DEFAULT 'Thanks — I have logged this report and will investigate it privately with the owner.',
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_group_jid (group_jid),
  KEY idx_echo_group_project (project_id),
  CONSTRAINT fk_echo_group_project FOREIGN KEY (project_id) REFERENCES echo_projects(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_repositories (
  id CHAR(29) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  project_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  owner_name VARCHAR(120) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  repository_name VARCHAR(180) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  repository_role ENUM('application','base','plugin','infrastructure','documentation') NOT NULL DEFAULT 'application',
  default_branch VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'main',
  installation_id BIGINT NOT NULL,
  read_only BOOLEAN NOT NULL DEFAULT TRUE,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  test_command VARCHAR(1000) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_repo (project_id, owner_name, repository_name),
  KEY idx_echo_repo_project (project_id),
  CONSTRAINT fk_echo_repo_project FOREIGN KEY (project_id) REFERENCES echo_projects(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_environments (
  id CHAR(29) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  project_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  name VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind ENUM('github_actions','webhook','manual') NOT NULL DEFAULT 'github_actions',
  requires_approval BOOLEAN NOT NULL DEFAULT TRUE,
  protected BOOLEAN NOT NULL DEFAULT TRUE,
  config_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_environment (project_id, name),
  CONSTRAINT fk_echo_environment_project FOREIGN KEY (project_id) REFERENCES echo_projects(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_incidents (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  project_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status ENUM('reported','triaging','awaiting_owner','change_approved','patching','staging','staging_verified','production','resolved','closed','failed') NOT NULL DEFAULT 'reported',
  severity ENUM('unknown','low','medium','high','critical') NOT NULL DEFAULT 'unknown',
  summary VARCHAR(500) NOT NULL,
  reporter_jid VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  reporter_name VARCHAR(255) NOT NULL DEFAULT '',
  group_jid VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_message_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_body TEXT NOT NULL,
  assigned_to VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  resolved_at DATETIME(6) NULL,
  UNIQUE KEY uq_echo_incident_message (group_jid, source_message_id),
  KEY idx_echo_incident_project_status (project_id, status, created_at),
  CONSTRAINT fk_echo_incident_project FOREIGN KEY (project_id) REFERENCES echo_projects(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_incident_messages (
  id CHAR(29) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  incident_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  whatsapp_message_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  chat_jid VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  sender_jid VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  sender_name VARCHAR(255) NOT NULL DEFAULT '',
  direction ENUM('inbound','outbound','internal') NOT NULL,
  body TEXT NOT NULL,
  metadata_json JSON NOT NULL,
  message_timestamp DATETIME(6) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_echo_incident_wa_message (chat_jid, whatsapp_message_id),
  KEY idx_echo_incident_messages (incident_id, created_at),
  CONSTRAINT fk_echo_message_incident FOREIGN KEY (incident_id) REFERENCES echo_incidents(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_analyses (
  id CHAR(29) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  incident_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status ENUM('complete','insufficient_evidence','failed') NOT NULL,
  confidence ENUM('none','low','medium','high') NOT NULL,
  summary TEXT NOT NULL,
  evidence_json JSON NOT NULL,
  proposal_json JSON NOT NULL,
  model VARCHAR(255) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  KEY idx_echo_analysis_incident (incident_id, created_at),
  CONSTRAINT fk_echo_analysis_incident FOREIGN KEY (incident_id) REFERENCES echo_incidents(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_change_sets (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  incident_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  repository_id CHAR(29) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  branch_name VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  commit_sha CHAR(40) CHARACTER SET ascii COLLATE ascii_bin NULL,
  pull_request_number BIGINT NULL,
  pull_request_url VARCHAR(1000) NOT NULL DEFAULT '',
  artifact_ref VARCHAR(500) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  status ENUM('proposed','preparing','testing','ready','failed','merged','closed') NOT NULL DEFAULT 'proposed',
  diff_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  KEY idx_echo_change_incident (incident_id),
  CONSTRAINT fk_echo_change_incident FOREIGN KEY (incident_id) REFERENCES echo_incidents(id) ON DELETE CASCADE,
  CONSTRAINT fk_echo_change_repo FOREIGN KEY (repository_id) REFERENCES echo_repositories(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_approvals (
  id CHAR(29) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  incident_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  action_type ENUM('prepare_patch','deploy_staging','deploy_production','rollback') NOT NULL,
  action_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  action_json JSON NOT NULL,
  nonce_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status ENUM('pending','approved','rejected','expired','consumed','cancelled') NOT NULL DEFAULT 'pending',
  requested_by VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  resolved_by VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  expires_at DATETIME(6) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  resolved_at DATETIME(6) NULL,
  KEY idx_echo_approval_status (status, expires_at),
  KEY idx_echo_approval_incident (incident_id, created_at),
  CONSTRAINT fk_echo_approval_incident FOREIGN KEY (incident_id) REFERENCES echo_incidents(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_deployments (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  incident_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  project_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  environment_name VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  artifact_ref VARCHAR(500) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status ENUM('queued','running','verified','failed','rolled_back','cancelled') NOT NULL DEFAULT 'queued',
  workflow_url VARCHAR(1000) NOT NULL DEFAULT '',
  started_at DATETIME(6) NULL,
  finished_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  KEY idx_echo_deployment_incident (incident_id, created_at),
  CONSTRAINT fk_echo_deployment_incident FOREIGN KEY (incident_id) REFERENCES echo_incidents(id) ON DELETE RESTRICT,
  CONSTRAINT fk_echo_deployment_project FOREIGN KEY (project_id) REFERENCES echo_projects(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_jobs (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  job_type VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  payload_json JSON NOT NULL,
  status ENUM('queued','running','complete','failed','dead') NOT NULL DEFAULT 'queued',
  attempts INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 5,
  available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  locked_by VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  locked_until DATETIME(6) NULL,
  last_error VARCHAR(1000) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  KEY idx_echo_jobs_claim (status, available_at, locked_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_whatsapp_outbox (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  chat_jid VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  body TEXT NOT NULL,
  status ENUM('queued','sending','sent','failed','dead') NOT NULL DEFAULT 'queued',
  attempts INT NOT NULL DEFAULT 0,
  available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  locked_until DATETIME(6) NULL,
  whatsapp_message_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  last_error VARCHAR(1000) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  sent_at DATETIME(6) NULL,
  KEY idx_echo_outbox_claim (status, available_at, locked_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
CREATE TABLE IF NOT EXISTS echo_audit_events (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  actor_type ENUM('whatsapp','admin','system','worker') NOT NULL,
  actor_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  action_name VARCHAR(120) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  resource_type VARCHAR(80) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  resource_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  outcome ENUM('attempted','succeeded','denied','failed') NOT NULL,
  metadata_json JSON NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  KEY idx_echo_audit_resource (resource_type, resource_id, created_at),
  KEY idx_echo_audit_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
