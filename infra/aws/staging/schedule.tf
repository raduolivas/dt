# Working hours: 08:00-20:00 Europe/Amsterdam, Monday-Friday. Outside them
# the host's Auto Scaling group is at 0 and the database is stopped.
# The database starts 10 minutes before the host so the services find it up.
# make staging-wake / staging-sleep do the same on demand (scripts/staging.sh).

locals {
  schedules = {
    db-start = {
      cron   = "cron(50 7 ? * MON-FRI *)"
      target = "arn:aws:scheduler:::aws-sdk:rds:startDBInstance"
      input  = { DbInstanceIdentifier = aws_db_instance.this.identifier }
    }
    host-start = {
      cron   = "cron(0 8 ? * MON-FRI *)"
      target = "arn:aws:scheduler:::aws-sdk:autoscaling:updateAutoScalingGroup"
      input  = { AutoScalingGroupName = aws_autoscaling_group.host.name, MinSize = 0, DesiredCapacity = 1 }
    }
    host-stop = {
      cron   = "cron(0 20 ? * MON-FRI *)"
      target = "arn:aws:scheduler:::aws-sdk:autoscaling:updateAutoScalingGroup"
      input  = { AutoScalingGroupName = aws_autoscaling_group.host.name, MinSize = 0, DesiredCapacity = 0 }
    }
    db-stop = {
      cron   = "cron(0 20 ? * MON-FRI *)"
      target = "arn:aws:scheduler:::aws-sdk:rds:stopDBInstance"
      input  = { DbInstanceIdentifier = aws_db_instance.this.identifier }
    }
  }
}

data "aws_iam_policy_document" "scheduler_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_iam_role" "scheduler" {
  name               = "dt-staging-scheduler"
  assume_role_policy = data.aws_iam_policy_document.scheduler_trust.json
}

data "aws_iam_policy_document" "scheduler" {
  statement {
    sid       = "StartStopDatabase"
    actions   = ["rds:StartDBInstance", "rds:StopDBInstance"]
    resources = [aws_db_instance.this.arn]
  }

  statement {
    sid       = "ScaleHost"
    actions   = ["autoscaling:UpdateAutoScalingGroup"]
    resources = [aws_autoscaling_group.host.arn]
  }
}

resource "aws_iam_role_policy" "scheduler" {
  name   = "start-stop"
  role   = aws_iam_role.scheduler.id
  policy = data.aws_iam_policy_document.scheduler.json
}

resource "aws_scheduler_schedule" "this" {
  for_each = local.schedules

  name                         = "dt-staging-${each.key}"
  schedule_expression          = each.value.cron
  schedule_expression_timezone = var.schedule_timezone
  state                        = var.schedule_enabled ? "ENABLED" : "DISABLED"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = each.value.target
    role_arn = aws_iam_role.scheduler.arn
    input    = jsonencode(each.value.input)

    # Starting a running database (or stopping a stopped one) fails; that
    # is the desired end state anyway, so no retries.
    retry_policy {
      maximum_retry_attempts = 0
    }
  }
}
