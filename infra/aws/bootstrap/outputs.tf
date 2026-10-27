output "staging_account_id" {
  description = "Account id of dt-staging."
  value       = aws_organizations_account.staging.id
}

output "production_account_id" {
  description = "Account id of dt-production (empty in M3)."
  value       = aws_organizations_account.production.id
}

output "deploy_role_arn_staging" {
  description = "Role GitHub Actions assumes (environment staging) to deploy."
  value       = one(aws_iam_role.deploy_staging[*].arn) # null until phase B
}

output "state_bucket" {
  description = "S3 bucket holding the tofu state of every stack (native locking)."
  value       = aws_s3_bucket.state.id
}

output "state_kms_key_arn" {
  description = "KMS key encrypting the state bucket; staging stacks pass it as kms_key_id."
  value       = aws_kms_key.state.arn
}

output "cloudtrail_bucket" {
  description = "Bucket with the organization trail logs."
  value       = aws_s3_bucket.trail.id
}

output "staging_task_role_names" {
  description = "Names the deploy role may pass to ECS. infra/aws/staging must create the roles with exactly these names."
  value = {
    cluster        = local.staging_cluster_name
    task_execution = local.staging_task_execution_role
    task           = local.staging_task_role
    ecr_prefix     = local.staging_ecr_repo_prefix
  }
}
