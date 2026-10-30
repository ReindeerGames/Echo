ALTER TABLE echo_approvals
  MODIFY COLUMN action_type ENUM('prepare_patch','deploy_staging','deploy_production','merge_production','rollback') NOT NULL;
