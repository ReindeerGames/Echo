ALTER TABLE echo_incidents
  ADD COLUMN evidence_revision BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER source_body;
-- echo:split
ALTER TABLE echo_analyses
  ADD COLUMN evidence_revision BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER incident_id,
  ADD KEY idx_echo_analysis_revision (incident_id, evidence_revision, created_at);
