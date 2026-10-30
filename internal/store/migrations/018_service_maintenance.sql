CREATE TABLE IF NOT EXISTS echo_service_maintenance_windows (
  id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  service_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  starts_at DATETIME(6) NOT NULL,
  ends_at DATETIME(6) NOT NULL,
  reason VARCHAR(300) NOT NULL,
  created_by VARCHAR(128) NOT NULL,
  cancelled_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_maintenance_service FOREIGN KEY (service_id) REFERENCES echo_services(id) ON DELETE CASCADE,
  INDEX idx_maintenance_service_window (service_id, starts_at, ends_at),
  INDEX idx_maintenance_current (cancelled_at, ends_at, starts_at),
  CONSTRAINT chk_maintenance_window CHECK (ends_at > starts_at)
) ENGINE=InnoDB;

-- echo:split

ALTER TABLE echo_service_alerts
  ADD COLUMN maintenance_window_id CHAR(28) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER resolved_at,
  ADD COLUMN notification_suppressed_until DATETIME(6) NULL AFTER maintenance_window_id,
  ADD COLUMN maintenance_opening_suppressed BOOLEAN NOT NULL DEFAULT FALSE AFTER notification_suppressed_until,
  ADD CONSTRAINT fk_alert_maintenance_window FOREIGN KEY (maintenance_window_id) REFERENCES echo_service_maintenance_windows(id) ON DELETE SET NULL,
  ADD INDEX idx_alert_suppression (notification_suppressed_until);
