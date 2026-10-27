resource "aws_budgets_budget" "monthly" {
  name         = "dt-monthly"
  budget_type  = "COST"
  limit_amount = var.budget_limit
  limit_unit   = var.budget_unit
  time_unit    = "MONTHLY"

  dynamic "notification" {
    for_each = [50, 80, 100]

    content {
      comparison_operator        = "GREATER_THAN"
      threshold                  = notification.value
      threshold_type             = "PERCENTAGE"
      notification_type          = "ACTUAL"
      subscriber_email_addresses = [var.alert_email]
    }
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.alert_email]
  }
}

# AWS may already have created a default SERVICE monitor, and only one is
# allowed per account: scripts/aws-bootstrap.sh then passes its ARN.
resource "aws_ce_anomaly_monitor" "services" {
  count = var.anomaly_monitor_arn == null ? 1 : 0

  name              = "dt-services"
  monitor_type      = "DIMENSIONAL"
  monitor_dimension = "SERVICE"
}

resource "aws_ce_anomaly_subscription" "alerts" {
  name             = "dt-anomaly-alerts"
  frequency        = "DAILY"
  monitor_arn_list = [var.anomaly_monitor_arn == null ? aws_ce_anomaly_monitor.services[0].arn : var.anomaly_monitor_arn]

  subscriber {
    type    = "EMAIL"
    address = var.alert_email
  }

  threshold_expression {
    dimension {
      key           = "ANOMALY_TOTAL_IMPACT_ABSOLUTE"
      match_options = ["GREATER_THAN_OR_EQUAL"]
      values        = [tostring(var.anomaly_threshold)]
    }
  }
}
