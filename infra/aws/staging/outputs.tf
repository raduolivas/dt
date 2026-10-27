# No output carries a secret value. Secrets are read in AWS (Secrets
# Manager) by the people and tasks allowed to, never through tofu output.

output "api_url" {
  description = "Public API base URL."
  value       = "https://${var.api_hostname}"
}

output "zone_name_servers" {
  description = "Name servers to delegate staging.deliveryontrack.io to at the registrar (runbook step 6)."
  value       = aws_route53_zone.staging.name_servers
}

output "alb_dns_name" {
  description = "ALB DNS name (api.<zone> is an alias to it)."
  value       = aws_lb.api.dns_name
}

output "cluster_name" {
  description = "ECS cluster."
  value       = aws_ecs_cluster.this.name
}

output "asg_name" {
  description = "Auto Scaling group of the ECS host (make staging-wake/sleep)."
  value       = aws_autoscaling_group.host.name
}

output "db_instance_id" {
  description = "RDS instance identifier (make staging-wake/sleep)."
  value       = aws_db_instance.this.identifier
}

output "ecr_repositories" {
  description = "ECR repository URLs by service."
  value       = { for s, r in aws_ecr_repository.svc : s => r.repository_url }
}

output "osrm_bucket" {
  description = "Bucket scripts/osrm-publish.sh uploads to."
  value       = aws_s3_bucket.osrm.bucket
}

output "secret_arns" {
  description = "ARNs (not values) of the Secrets Manager secrets; dtctl material is in dt-staging/mtls and dt-staging/app."
  value = {
    app  = aws_secretsmanager_secret.app.arn
    db   = aws_secretsmanager_secret.db.arn
    mtls = aws_secretsmanager_secret.mtls.arn
  }
}
