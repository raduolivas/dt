# Email alarms (the budget alarms live in the bootstrap stack). Missing data
# (the host asleep, no traffic) never alarms.

resource "aws_sns_topic" "alerts" {
  name              = "dt-staging-alerts"
  kms_master_key_id = aws_kms_key.data.arn
}

resource "aws_sns_topic_subscription" "alerts_email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email # confirm the subscription mail once
}

resource "aws_cloudwatch_metric_alarm" "host_cpu" {
  alarm_name          = "dt-staging-host-cpu"
  alarm_description   = "ECS host CPU above 80 % for 10 minutes"
  namespace           = "AWS/EC2"
  metric_name         = "CPUUtilization"
  dimensions          = { AutoScalingGroupName = aws_autoscaling_group.host.name }
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  threshold           = 80
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_metric_alarm" "host_memory" {
  alarm_name          = "dt-staging-host-memory"
  alarm_description   = "ECS host memory above 90 % (CloudWatch agent)"
  namespace           = "CWAgent"
  metric_name         = "mem_used_percent"
  dimensions          = { AutoScalingGroupName = aws_autoscaling_group.host.name }
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  threshold           = 90
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_metric_alarm" "alb_5xx" {
  alarm_name          = "dt-staging-alb-5xx"
  alarm_description   = "More than 5 HTTP 5xx answers (ALB or targets) in 5 minutes"
  evaluation_periods  = 1
  threshold           = 5
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]

  metric_query {
    id          = "total"
    expression  = "FILL(elb, 0) + FILL(target, 0)"
    label       = "5xx"
    return_data = true
  }

  metric_query {
    id = "elb"

    metric {
      namespace   = "AWS/ApplicationELB"
      metric_name = "HTTPCode_ELB_5XX_Count"
      dimensions  = { LoadBalancer = aws_lb.api.arn_suffix }
      stat        = "Sum"
      period      = 300
    }
  }

  metric_query {
    id = "target"

    metric {
      namespace   = "AWS/ApplicationELB"
      metric_name = "HTTPCode_Target_5XX_Count"
      dimensions  = { LoadBalancer = aws_lb.api.arn_suffix }
      stat        = "Sum"
      period      = 300
    }
  }
}

resource "aws_cloudwatch_metric_alarm" "rds_storage" {
  alarm_name          = "dt-staging-rds-free-storage"
  alarm_description   = "RDS free storage below 2 GB"
  namespace           = "AWS/RDS"
  metric_name         = "FreeStorageSpace"
  dimensions          = { DBInstanceIdentifier = aws_db_instance.this.identifier }
  statistic           = "Minimum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 2 * 1024 * 1024 * 1024
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
}
