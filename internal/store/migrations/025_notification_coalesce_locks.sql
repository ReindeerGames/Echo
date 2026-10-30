CREATE TABLE echo_notification_coalesce_locks (
  lock_key VARCHAR(120) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
-- echo:split
INSERT INTO echo_notification_coalesce_locks(lock_key) VALUES
  ('monitor-alert-lifecycle'),
  ('monitor-transition:opened'),
  ('monitor-transition:reopened'),
  ('monitor-transition:escalated'),
  ('monitor-transition:resolved'),
  ('monitor-transition:maintenance_ended'),
  ('monitor-transition:reminder');
