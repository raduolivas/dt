data "aws_caller_identity" "current" {}

# Customer-managed key for everything that holds staging data: Secrets
# Manager (DATA_KEYS, signing keys, passwords, mTLS keys), RDS storage and
# its master secret, and the alarm topic. Survives `make staging-down`.
resource "aws_kms_key" "data" {
  description             = "DT staging data (Secrets Manager, RDS, SNS)"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.data_key.json

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "data" {
  name          = "alias/dt-staging-data"
  target_key_id = aws_kms_key.data.key_id
}

data "aws_iam_policy_document" "data_key" {
  # The account administers and uses the key through IAM policies (the
  # execution role's Decrypt is granted there, scoped to Secrets Manager).
  statement {
    sid       = "AccountAdministers"
    effect    = "Allow"
    actions   = ["kms:*"]
    resources = ["*"] # in a key policy, "*" means this key

    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
  }

  # CloudWatch alarms publish to the encrypted SNS topic.
  statement {
    sid       = "CloudWatchAlarmsPublish"
    effect    = "Allow"
    actions   = ["kms:Decrypt", "kms:GenerateDataKey*"]
    resources = ["*"]

    principals {
      type        = "Service"
      identifiers = ["cloudwatch.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}
