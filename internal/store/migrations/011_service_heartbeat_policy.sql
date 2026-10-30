ALTER TABLE echo_services
  ADD COLUMN IF NOT EXISTS heartbeat_timeout_seconds INT UNSIGNED NOT NULL DEFAULT 0 AFTER enabled;
-- echo:split
ALTER TABLE echo_services
  ADD INDEX IF NOT EXISTS idx_echo_service_heartbeat (enabled, heartbeat_timeout_seconds);
