resource "aws_db_subnet_group" "this" {
  name       = "dt-staging"
  subnet_ids = aws_subnet.db[*].id
}

resource "aws_db_parameter_group" "this" {
  name   = "dt-staging-pg18"
  family = "postgres18"

  parameter {
    name         = "rds.force_ssl"
    value        = "1"
    apply_method = "pending-reboot"
  }

  parameter {
    name  = "log_min_duration_statement"
    value = "1000" # log statements slower than 1 s (no parameters: log_parameter_max_length stays default)
  }
}

# Staging data is sandbox and simulated only (spec section 7.1), so the
# instance is single-AZ, has no deletion protection and no final snapshot:
# `make staging-down` must be able to remove it.
# trivy:ignore:AVD-AWS-0177 Teardown is a feature of staging (make staging-down); it holds no real data.
# trivy:ignore:AVD-AWS-0176 The services authenticate with per-role passwords from Secrets Manager; IAM database auth would need token refresh in every service.
# trivy:ignore:AVD-AWS-0133 Performance Insights is not needed on a db.t4g.micro staging database.
resource "aws_db_instance" "this" {
  identifier     = "dt-staging"
  engine         = "postgres"
  engine_version = "18" # a prefix: auto_minor_version_upgrade picks the minor
  instance_class = "db.t4g.micro"

  allocated_storage     = 20
  max_allocated_storage = 0 # no autoscaling: the alarm fires below 2 GB free instead
  storage_type          = "gp3"
  storage_encrypted     = true
  kms_key_id            = aws_kms_key.data.arn

  db_name  = "dt"
  username = "dt_admin"
  # RDS generates the master password and keeps it in its own secret,
  # encrypted with the same key. Only the db-bootstrap task reads it.
  manage_master_user_password   = true
  master_user_secret_kms_key_id = aws_kms_key.data.key_id

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.rds.id]
  parameter_group_name   = aws_db_parameter_group.this.name
  publicly_accessible    = false
  multi_az               = false

  # The schedule stops the database 20:00-08:00 and at weekends; backups and
  # maintenance only run while it is up, so both windows are in the day (UTC).
  backup_retention_period    = 3
  backup_window              = "11:00-11:30"
  maintenance_window         = "tue:11:30-tue:12:00"
  copy_tags_to_snapshot      = true
  auto_minor_version_upgrade = true
  apply_immediately          = true

  deletion_protection      = false
  skip_final_snapshot      = true
  delete_automated_backups = true

  performance_insights_enabled = false
}
