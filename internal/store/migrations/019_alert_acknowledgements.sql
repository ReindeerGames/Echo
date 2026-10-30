ALTER TABLE echo_service_alerts
  ADD COLUMN acknowledged_at DATETIME(6) NULL AFTER maintenance_opening_suppressed,
  ADD COLUMN acknowledged_via VARCHAR(24) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' AFTER acknowledged_at,
  ADD COLUMN last_notified_at DATETIME(6) NULL AFTER acknowledged_via,
  ADD COLUMN reminder_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER last_notified_at,
  ADD INDEX idx_alert_reminder (alert_status, severity, acknowledged_at, last_notified_at);

-- echo:split

UPDATE echo_service_alerts
SET last_notified_at = UTC_TIMESTAMP(6)
WHERE alert_status = 'open' AND last_notified_at IS NULL;
