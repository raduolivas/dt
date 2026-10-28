# One t3.medium ECS host in an Auto Scaling group (min 0, max 1) that the
# schedule scales to 0 at night. Bridge networking, static host ports.

locals {
  cluster_name = "dt-staging" # fixed by the bootstrap's deploy role
}

data "aws_ssm_parameter" "ecs_ami" {
  name = "/aws/service/ecs/optimized-ami/amazon-linux-2023/recommended/image_id"
}

resource "aws_ecs_cluster" "this" {
  name = local.cluster_name

  # trivy:ignore:AVD-AWS-0034 Container Insights costs more than the host; the memory and CPU alarms cover staging.
  setting {
    name  = "containerInsights"
    value = "disabled"
  }
}

# ---- IAM: host ----------------------------------------------------------------

data "aws_iam_policy_document" "ec2_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "host" {
  name               = "dt-staging-ecs-host"
  assume_role_policy = data.aws_iam_policy_document.ec2_trust.json
}

resource "aws_iam_role_policy_attachment" "host" {
  for_each = toset([
    "arn:aws:iam::aws:policy/service-role/AmazonEC2ContainerServiceforEC2Role", # ECS agent
    "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",                     # Session Manager, no SSH
    "arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy",                      # memory metric
  ])

  role       = aws_iam_role.host.name
  policy_arn = each.value
}

resource "aws_iam_instance_profile" "host" {
  name = "dt-staging-ecs-host"
  role = aws_iam_role.host.name
}

# ---- IAM: tasks (names fixed by the bootstrap's deploy role PassRole) --------

data "aws_iam_policy_document" "ecs_tasks_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_iam_role" "task_execution" {
  name               = "dt-staging-task-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_trust.json
}

data "aws_iam_policy_document" "task_execution" {
  statement {
    sid       = "EcrLogin"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"] # no resource-level permissions for this action
  }

  statement {
    sid       = "EcrPull"
    actions   = ["ecr:BatchCheckLayerAvailability", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"]
    resources = [for r in aws_ecr_repository.svc : r.arn]
  }

  statement {
    sid       = "Logs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.staging.arn}:*"]
  }

  statement {
    sid     = "Secrets"
    actions = ["secretsmanager:GetSecretValue"]
    resources = [
      aws_secretsmanager_secret.app.arn,
      aws_secretsmanager_secret.db.arn,
      aws_secretsmanager_secret.mtls.arn,
      aws_db_instance.this.master_user_secret[0].secret_arn,
    ]
  }

  statement {
    sid       = "DecryptSecrets"
    actions   = ["kms:Decrypt"]
    resources = [aws_kms_key.data.arn]

    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["secretsmanager.${var.region}.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "task_execution" {
  name   = "pull-logs-secrets"
  role   = aws_iam_role.task_execution.id
  policy = data.aws_iam_policy_document.task_execution.json
}

# The containers' own role. Only the OSRM data sync needs AWS at all.
resource "aws_iam_role" "task" {
  name               = "dt-staging-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_trust.json
}

data "aws_iam_policy_document" "task" {
  statement {
    sid       = "OsrmList"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.osrm.arn]
  }

  statement {
    sid       = "OsrmRead"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.osrm.arn}/*"]
  }
}

resource "aws_iam_role_policy" "task" {
  name   = "osrm-read"
  role   = aws_iam_role.task.id
  policy = data.aws_iam_policy_document.task.json
}

# ---- host -----------------------------------------------------------------------

resource "aws_launch_template" "host" {
  name                   = "dt-staging-ecs-host"
  image_id               = data.aws_ssm_parameter.ecs_ami.insecure_value
  instance_type          = var.instance_type
  update_default_version = true
  # No key pair: access is SSM Session Manager only.

  iam_instance_profile {
    arn = aws_iam_instance_profile.host.arn
  }

  # IMDSv2 only, and a hop limit of 1 so bridge-mode containers cannot reach
  # the instance role (they get the task role from the ECS agent instead).
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
    instance_metadata_tags      = "disabled"
  }

  block_device_mappings {
    device_name = "/dev/xvda"

    ebs {
      volume_size           = 30
      volume_type           = "gp3"
      encrypted             = true
      delete_on_termination = true
    }
  }

  network_interfaces {
    associate_public_ip_address = true # no NAT gateway: the host egresses directly
    security_groups             = [aws_security_group.host.id]
    delete_on_termination       = true
  }

  user_data = base64encode(templatefile("${path.module}/files/user_data.sh.tftpl", {
    cluster = local.cluster_name
  }))

  tag_specifications {
    resource_type = "instance"
    tags = {
      Name      = "dt-staging-ecs-host"
      project   = "dt"
      stack     = "staging"
      managedby = "opentofu"
    }
  }

  tag_specifications {
    resource_type = "volume"
    tags = {
      Name      = "dt-staging-ecs-host"
      project   = "dt"
      stack     = "staging"
      managedby = "opentofu"
    }
  }
}

resource "aws_autoscaling_group" "host" {
  name                = "dt-staging-ecs-host"
  min_size            = 0
  max_size            = 1
  desired_capacity    = 1
  vpc_zone_identifier = aws_subnet.public[*].id
  health_check_type   = "EC2"

  launch_template {
    id      = aws_launch_template.host.id
    version = "$Latest" # a new ECS AMI applies at the next morning start
  }

  tag {
    key                 = "AmazonECSManaged"
    value               = "true"
    propagate_at_launch = true
  }

  lifecycle {
    # The schedule and make staging-wake/sleep move these.
    ignore_changes = [desired_capacity, min_size]
  }
}

# Managed scaling stays off: it would start the host at night as soon as a
# task is pending. The schedule (and make staging-wake) own the capacity.
resource "aws_ecs_capacity_provider" "host" {
  name = "dt-staging-host"

  auto_scaling_group_provider {
    auto_scaling_group_arn         = aws_autoscaling_group.host.arn
    managed_termination_protection = "DISABLED"

    managed_scaling {
      status = "DISABLED"
    }
  }
}

resource "aws_ecs_cluster_capacity_providers" "this" {
  cluster_name       = aws_ecs_cluster.this.name
  capacity_providers = [aws_ecs_capacity_provider.host.name]

  default_capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.host.name
    weight            = 1
    base              = 0
  }
}

# trivy:ignore:AVD-AWS-0017 Logs are PII-scrubbed by the services; the CMK is reserved for secrets and data.
resource "aws_cloudwatch_log_group" "staging" {
  name              = "/dt/staging"
  retention_in_days = 14
}
