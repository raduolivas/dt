# GitHub Actions deploys to staging through OIDC: no long-lived keys anywhere.
# Everything here lives in the dt-staging account (provider alias "staging").

locals {
  staging_account_id = aws_organizations_account.staging.id

  # Task 14 (infra/aws/staging) must create these roles with exactly these
  # names, the cluster as dt-staging, and ECR repositories named dt-*.
  staging_cluster_name        = "dt-staging"
  staging_task_execution_role = "dt-staging-task-execution"
  staging_task_role           = "dt-staging-task"
  staging_ecr_repo_prefix     = "dt-"
}

resource "aws_iam_openid_connect_provider" "github_staging" {
  provider = aws.staging
  count    = var.enable_staging_oidc ? 1 : 0

  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

data "aws_iam_policy_document" "deploy_staging_trust" {
  count = var.enable_staging_oidc ? 1 : 0

  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github_staging[0].arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${var.github_repository}:environment:staging"]
    }
  }
}

data "aws_iam_policy_document" "deploy_staging" {
  statement {
    sid       = "EcrLogin"
    effect    = "Allow"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"] # the action does not support resource-level permissions
  }

  statement {
    sid    = "EcrPush"
    effect = "Allow"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:CompleteLayerUpload",
      "ecr:DescribeImages",
      "ecr:GetDownloadUrlForLayer",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
    ]
    resources = ["arn:aws:ecr:${var.region}:${local.staging_account_id}:repository/${local.staging_ecr_repo_prefix}*"]
  }

  statement {
    sid    = "EcsDeployServices"
    effect = "Allow"
    actions = [
      "ecs:DescribeServices",
      "ecs:UpdateService",
    ]
    resources = ["arn:aws:ecs:${var.region}:${local.staging_account_id}:service/${local.staging_cluster_name}/*"]
  }

  statement {
    sid       = "EcsDescribeCluster"
    effect    = "Allow"
    actions   = ["ecs:DescribeClusters"]
    resources = ["arn:aws:ecs:${var.region}:${local.staging_account_id}:cluster/${local.staging_cluster_name}"]
  }

  statement {
    sid    = "EcsMigrationTask"
    effect = "Allow"
    actions = [
      "ecs:RunTask",
    ]
    resources = ["arn:aws:ecs:${var.region}:${local.staging_account_id}:task-definition/dt-staging-*:*"]

    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = ["arn:aws:ecs:${var.region}:${local.staging_account_id}:cluster/${local.staging_cluster_name}"]
    }
  }

  statement {
    sid    = "EcsWatchTasks"
    effect = "Allow"
    actions = [
      "ecs:DescribeTasks",
      "ecs:StopTask",
    ]
    resources = ["arn:aws:ecs:${var.region}:${local.staging_account_id}:task/${local.staging_cluster_name}/*"]
  }

  statement {
    sid    = "EcsTaskDefinitions"
    effect = "Allow"
    actions = [
      "ecs:DeregisterTaskDefinition",
      "ecs:DescribeTaskDefinition",
      "ecs:RegisterTaskDefinition",
    ]
    resources = ["*"] # task-definition actions do not support resource-level permissions
  }

  statement {
    sid     = "PassTaskRolesToEcs"
    effect  = "Allow"
    actions = ["iam:PassRole"]
    resources = [
      "arn:aws:iam::${local.staging_account_id}:role/${local.staging_task_execution_role}",
      "arn:aws:iam::${local.staging_account_id}:role/${local.staging_task_role}",
    ]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "deploy_staging" {
  provider = aws.staging
  count    = var.enable_staging_oidc ? 1 : 0

  name                 = "dt-deploy-staging"
  assume_role_policy   = data.aws_iam_policy_document.deploy_staging_trust[0].json
  max_session_duration = 3600
}

resource "aws_iam_role_policy" "deploy_staging" {
  provider = aws.staging
  count    = var.enable_staging_oidc ? 1 : 0

  name   = "deploy"
  role   = aws_iam_role.deploy_staging[0].id
  policy = data.aws_iam_policy_document.deploy_staging.json
}
