# Task definitions and services. Network mode bridge with static host ports:
# every container resolves the names below to the host's docker0 address
# (extraHosts), so URLs read like the local Compose stack's.
#
# Memory reservations (container memoryReservation, what ECS reserves on the
# host when placing a task) follow the plan's table:
#   gateway 256 + deliveries 192 + tracking 320 + webhooks 96 + accounts 160
#   + nats 256 + redis 128 + osrm-car 900 + osrm-bike 900 = 3208 MB,
# plus the short-lived init containers (4 x certs 16, 2 x OSRM fetch 32)
# = 3336 MB <= 3.4 GB. Hard limits (container memory) are higher, so a spike
# or the OSRM graph load is not OOM-killed at the reservation.
# The one-off migrate and db-bootstrap tasks run before a rollout (the
# deployment stops the old task first: minimum healthy 0 %).

locals {
  images = {
    busybox  = "public.ecr.aws/docker/library/busybox:1.37"
    awscli   = "public.ecr.aws/aws-cli/aws-cli:2.31.0"
    postgres = "public.ecr.aws/docker/library/postgres:18-alpine"
    nats     = "public.ecr.aws/docker/library/nats:2.12-alpine"
    redis    = "public.ecr.aws/docker/library/redis:8.2-alpine"
    # Same digest as infra/compose and scripts/osrm-prepare.sh (the data format must match).
    osrm = "osrm/osrm-backend@sha256:af5d4a83fb90086a43b1ae2ca22872e6768766ad5fcbb07a29ff90ec644ee409"
  }

  # memoryReservation (MB) of each main container: the plan's table.
  memory = {
    gateway    = 256
    deliveries = 192
    tracking   = 320
    webhooks   = 96
    accounts   = 160
    nats       = 256
    redis      = 128
    osrm-car   = 900
    osrm-bike  = 900
  }

  # Hard limit (container memory, MB): the container is killed above it.
  memory_limit = {
    gateway    = 512
    deliveries = 384
    tracking   = 640
    webhooks   = 256
    accounts   = 320
    nats       = 512
    redis      = 256
    osrm-car   = 1536
    osrm-bike  = 1536
  }

  # Init containers: reservation and hard limit.
  init_memory = {
    certs = { reservation = 16, limit = 32 }
    fetch = { reservation = 32, limit = 256 }
  }

  extra_hosts = [
    for h in ["gateway", "deliveries", "tracking", "webhooks", "accounts", "nats", "redis", "osrm-car", "osrm-bike"] :
    { hostname = h, ipAddress = var.docker_bridge_ip }
  ]

  # valueFrom for one JSON key of a secret.
  app_key  = { for k in keys(local.app_secret) : k => "${aws_secretsmanager_secret.app.arn}:${k}::" }
  db_key   = { for k in keys(local.db_secret) : k => "${aws_secretsmanager_secret.db.arn}:${k}::" }
  mtls_key = { for k in keys(local.mtls_secret) : k => "${aws_secretsmanager_secret.mtls.arn}:${k}::" }

  internal = {
    nats_url       = "nats://nats:${local.ports.nats}"
    redis_url      = "redis://redis:${local.ports.redis}/0"
    deliveries_rpc = "https://deliveries:${local.ports.deliveries}"
    tracking_rpc   = "https://tracking:${local.ports.tracking_rpc}"
  }

  # Per service: plain environment, secrets (env name => valueFrom), host
  # ports, and whether it gets the internal mTLS material in /certs.
  app = {
    gateway = {
      ports = [local.ports.gateway]
      mtls  = true
      env = {
        PORT                       = tostring(local.ports.gateway)
        REDIS_URL                  = local.internal.redis_url
        NATS_URL                   = local.internal.nats_url
        TRUSTED_PROXIES            = var.vpc_cidr # the ALB's nodes
        DELIVERIES_RPC_URL         = local.internal.deliveries_rpc
        TRACKING_RPC_URL           = local.internal.tracking_rpc
        TRACKING_URL_BASE          = var.tracking_url_base
        DATA_KEY_CURRENT           = "gw1"
        VIEWER_SIGNING_KEY_CURRENT = "vw1"
      }
      secrets = {
        DATABASE_URL        = local.db_key["svc_gateway_url"]
        APIKEY_PEPPER       = local.app_key["apikey_pepper"]
        INTERNAL_RPC_TOKEN  = local.app_key["internal_rpc_token"]
        DATA_KEYS           = local.app_key["gateway_data_keys"]
        VIEWER_SIGNING_KEYS = local.app_key["viewer_signing_keys"]
      }
    }
    deliveries = {
      ports = [local.ports.deliveries]
      mtls  = true
      env = {
        PORT             = tostring(local.ports.deliveries)
        NATS_URL         = local.internal.nats_url
        DATA_KEY_CURRENT = "dk1"
      }
      secrets = {
        DATABASE_URL       = local.db_key["svc_deliveries_url"]
        INTERNAL_RPC_TOKEN = local.app_key["internal_rpc_token"]
        DATA_KEYS          = local.app_key["deliveries_data_keys"]
      }
    }
    tracking = {
      ports = [local.ports.tracking, local.ports.tracking_rpc]
      mtls  = true
      env = {
        PORT               = tostring(local.ports.tracking)
        INTERNAL_RPC_PORT  = tostring(local.ports.tracking_rpc)
        REDIS_URL          = local.internal.redis_url
        NATS_URL           = local.internal.nats_url
        OSRM_CAR_URL       = "http://osrm-car:${local.ports.osrm_car}"
        OSRM_BIKE_URL      = "http://osrm-bike:${local.ports.osrm_bike}"
        DELIVERIES_RPC_URL = local.internal.deliveries_rpc
        JWKS_URL           = "http://gateway:${local.ports.gateway}/.well-known/jwks.json"
      }
      secrets = {
        DATABASE_URL       = local.db_key["svc_tracking_url"]
        INTERNAL_RPC_TOKEN = local.app_key["internal_rpc_token"]
      }
    }
    webhooks = {
      ports = [local.ports.webhooks]
      mtls  = false
      env = {
        PORT = tostring(local.ports.webhooks)
      }
      secrets = {
        DATABASE_URL = local.db_key["svc_webhooks_url"]
      }
    }
    accounts = {
      ports = [local.ports.accounts]
      mtls  = true
      env = {
        PORT     = tostring(local.ports.accounts)
        NATS_URL = local.internal.nats_url
      }
      secrets = {
        DATABASE_URL       = local.db_key["svc_accounts_url"]
        APIKEY_PEPPER      = local.app_key["apikey_pepper"]
        INTERNAL_RPC_TOKEN = local.app_key["internal_rpc_token"]
      }
    }
  }

  mtls_env = {
    INTERNAL_RPC_TLS_CERT = "/certs/tls.pem"
    INTERNAL_RPC_TLS_KEY  = "/certs/tls.key"
    INTERNAL_RPC_TLS_CA   = "/certs/ca.pem"
  }

  log_options = {
    awslogs-group  = aws_cloudwatch_log_group.staging.name
    awslogs-region = var.region
  }

  # Writes the service's certificate, key and the CA from Secrets Manager to
  # a task-scoped tmpfs volume (the app images are shell-less), then exits.
  certs_container = { for s, a in local.app : s => {
    name                   = "certs"
    image                  = local.images.busybox
    essential              = false
    memoryReservation      = local.init_memory.certs.reservation
    memory                 = local.init_memory.certs.limit
    user                   = "65532:65532" # the distroless nonroot user, owner of the tmpfs
    readonlyRootFilesystem = true
    command = ["sh", "-c", join(" && ", [
      "umask 077",
      "printf '%s' \"$TLS_CERT\" >/certs/tls.pem",
      "printf '%s' \"$TLS_KEY\" >/certs/tls.key",
      "printf '%s' \"$TLS_CA\" >/certs/ca.pem",
    ])]
    secrets = [
      { name = "TLS_CERT", valueFrom = local.mtls_key["${s}_pem"] },
      { name = "TLS_KEY", valueFrom = local.mtls_key["${s}_key"] },
      { name = "TLS_CA", valueFrom = local.mtls_key["ca_pem"] },
    ]
    mountPoints      = [{ sourceVolume = "certs", containerPath = "/certs", readOnly = false }]
    linuxParameters  = { capabilities = { drop = ["ALL"] } }
    logConfiguration = { logDriver = "awslogs", options = merge(local.log_options, { awslogs-stream-prefix = "${s}-certs" }) }
  } if a.mtls }

  # The service container. "run" serves with MIGRATIONS=skip and only its
  # svc_<svc> URL; "migrate" is the one-off variant: MIGRATIONS=only, the
  # mig_<svc> URL (schema owner), no host ports. Only the migrate tasks ever
  # hold the schema owner's credentials.
  app_container = { for pair in setproduct(keys(local.app), [false, true]) : "${pair[0]}/${pair[1] ? "migrate" : "run"}" => {
    name                   = pair[0]
    image                  = "${aws_ecr_repository.svc[pair[0]].repository_url}:${var.image_tag}"
    essential              = true
    memoryReservation      = local.memory[pair[0]]
    memory                 = local.memory_limit[pair[0]]
    readonlyRootFilesystem = true
    stopTimeout            = 30
    dockerSecurityOptions  = ["no-new-privileges"]
    linuxParameters        = { capabilities = { drop = ["ALL"] } }
    extraHosts             = local.extra_hosts
    portMappings           = pair[1] ? [] : [for p in local.app[pair[0]].ports : { containerPort = p, hostPort = p, protocol = "tcp" }]
    environment = [for k, v in merge(
      { LOG_LEVEL = "info" },
      local.app[pair[0]].env,
      local.app[pair[0]].mtls ? local.mtls_env : {},
      { MIGRATIONS = pair[1] ? "only" : "skip" },
    ) : { name = k, value = v }]
    secrets = [for k, v in merge(
      local.app[pair[0]].secrets,
      pair[1] ? { MIGRATIONS_DATABASE_URL = local.db_key["mig_${pair[0]}_url"] } : {},
    ) : { name = k, valueFrom = v }]
    mountPoints      = local.app[pair[0]].mtls ? [{ sourceVolume = "certs", containerPath = "/certs", readOnly = true }] : []
    dependsOn        = local.app[pair[0]].mtls ? [{ containerName = "certs", condition = "SUCCESS" }] : []
    logConfiguration = { logDriver = "awslogs", options = merge(local.log_options, { awslogs-stream-prefix = pair[1] ? "migrate-${pair[0]}" : pair[0] }) }
  } }
}

# ---- application services -------------------------------------------------------

resource "aws_ecs_task_definition" "app" {
  for_each = local.app

  family                   = "dt-staging-${each.key}"
  requires_compatibilities = ["EC2"]
  network_mode             = "bridge"
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.task.arn

  container_definitions = jsonencode(concat(
    each.value.mtls ? [local.certs_container[each.key]] : [],
    [local.app_container["${each.key}/run"]],
  ))

  dynamic "volume" {
    for_each = each.value.mtls ? [1] : []

    content {
      name = "certs"

      docker_volume_configuration {
        scope  = "task"
        driver = "local"
        driver_opts = {
          type   = "tmpfs"
          device = "tmpfs"
          o      = "size=1m,uid=65532,gid=65532,mode=0700"
        }
      }
    }
  }
}

# migrate-<svc>: the same container with MIGRATIONS=only; the deploy runs
# it and waits for exit code 0 before updating the service.
resource "aws_ecs_task_definition" "migrate" {
  for_each = local.app

  family                   = "dt-staging-migrate-${each.key}"
  requires_compatibilities = ["EC2"]
  network_mode             = "bridge"
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.task.arn

  container_definitions = jsonencode(concat(
    each.value.mtls ? [local.certs_container[each.key]] : [],
    [local.app_container["${each.key}/migrate"]],
  ))

  dynamic "volume" {
    for_each = each.value.mtls ? [1] : []

    content {
      name = "certs"

      docker_volume_configuration {
        scope  = "task"
        driver = "local"
        driver_opts = {
          type   = "tmpfs"
          device = "tmpfs"
          o      = "size=1m,uid=65532,gid=65532,mode=0700"
        }
      }
    }
  }
}

locals {
  lb_target = {
    gateway  = { tg = aws_lb_target_group.gateway.arn, port = local.ports.gateway }
    tracking = { tg = aws_lb_target_group.tracking.arn, port = local.ports.tracking }
  }
}

resource "aws_ecs_service" "app" {
  for_each = local.app

  name            = each.key
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.app[each.key].arn
  desired_count   = 1

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.host.name
    weight            = 1
  }

  # One host and static ports: the old task stops before the new one starts.
  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  # No tags from ECS: the deploy role has no ecs:TagResource.
  enable_ecs_managed_tags = false
  propagate_tags          = "NONE"

  health_check_grace_period_seconds = contains(keys(local.lb_target), each.key) ? 60 : null

  dynamic "load_balancer" {
    for_each = contains(keys(local.lb_target), each.key) ? [local.lb_target[each.key]] : []

    content {
      target_group_arn = load_balancer.value.tg
      container_name   = each.key
      container_port   = load_balancer.value.port
    }
  }

  lifecycle {
    # The deploy workflow registers new revisions (image tag = commit SHA).
    ignore_changes = [task_definition]
  }

  depends_on = [aws_lb_listener.https, aws_ecs_cluster_capacity_providers.this]
}

# ---- one-off: database bootstrap -------------------------------------------------

resource "aws_ecs_task_definition" "db_bootstrap" {
  family                   = "dt-staging-db-bootstrap"
  requires_compatibilities = ["EC2"]
  network_mode             = "bridge"
  execution_role_arn       = aws_iam_role.task_execution.arn

  container_definitions = jsonencode([{
    name                   = "db-bootstrap"
    image                  = local.images.postgres
    essential              = true
    memoryReservation      = 64
    memory                 = 128
    user                   = "postgres"
    readonlyRootFilesystem = true
    linuxParameters        = { capabilities = { drop = ["ALL"] } }
    command                = ["sh", "-c", file("${path.module}/files/db-bootstrap.sh")]
    environment = [
      { name = "PGHOST", value = aws_db_instance.this.address },
      { name = "PGDATABASE", value = "dt" },
      { name = "PGUSER", value = aws_db_instance.this.username },
      { name = "PGSSLMODE", value = "require" },
      { name = "PGCONNECT_TIMEOUT", value = "10" },
      # The existing role and schema SQL of the local stack, verbatim.
      { name = "BOOTSTRAP_SQL", value = file("${path.module}/../../compose/postgres/init/01-schemas.sql") },
    ]
    secrets = concat(
      [{ name = "PGPASSWORD", valueFrom = "${aws_db_instance.this.master_user_secret[0].secret_arn}:password::" }],
      [for r in local.db_roles : { name = upper("${r}_password"), valueFrom = local.db_key["${r}_password"] }],
    )
    logConfiguration = { logDriver = "awslogs", options = merge(local.log_options, { awslogs-stream-prefix = "db-bootstrap" }) }
  }])
}

# ---- infrastructure containers -----------------------------------------------------

resource "aws_ecs_task_definition" "nats" {
  family                   = "dt-staging-nats"
  requires_compatibilities = ["EC2"]
  network_mode             = "bridge"

  container_definitions = jsonencode([{
    name                   = "nats"
    image                  = local.images.nats
    essential              = true
    memoryReservation      = local.memory.nats
    memory                 = local.memory_limit.nats
    readonlyRootFilesystem = true
    command                = ["-js", "-sd", "/data", "-m", "8222"]
    portMappings           = [{ containerPort = 4222, hostPort = local.ports.nats, protocol = "tcp" }]
    mountPoints            = [{ sourceVolume = "nats-data", containerPath = "/data", readOnly = false }]
    healthCheck = {
      command     = ["CMD", "wget", "-qO-", "http://localhost:8222/healthz"]
      interval    = 10
      timeout     = 3
      retries     = 3
      startPeriod = 10
    }
    logConfiguration = { logDriver = "awslogs", options = merge(local.log_options, { awslogs-stream-prefix = "nats" }) }
  }])

  # JetStream on the host's EBS volume. The instance is replaced every
  # working day, so streams start empty each morning (runbook).
  volume {
    name      = "nats-data"
    host_path = "/data/nats"
  }
}

resource "aws_ecs_task_definition" "redis" {
  family                   = "dt-staging-redis"
  requires_compatibilities = ["EC2"]
  network_mode             = "bridge"

  container_definitions = jsonencode([{
    name                   = "redis"
    image                  = local.images.redis
    essential              = true
    memoryReservation      = local.memory.redis
    memory                 = local.memory_limit.redis
    readonlyRootFilesystem = true
    # Ephemeral: no snapshots, no AOF; noeviction keeps revocations from
    # being evicted silently.
    command      = ["redis-server", "--save", "", "--appendonly", "no", "--maxmemory", "100mb", "--maxmemory-policy", "noeviction"]
    portMappings = [{ containerPort = 6379, hostPort = local.ports.redis, protocol = "tcp" }]
    healthCheck = {
      command  = ["CMD", "redis-cli", "ping"]
      interval = 10
      timeout  = 3
      retries  = 3
    }
    logConfiguration = { logDriver = "awslogs", options = merge(local.log_options, { awslogs-stream-prefix = "redis" }) }
  }])
}

# ---- OSRM (Noord-Holland, car and bike) ------------------------------------------------

locals {
  osrm = {
    osrm-car  = { profile = "car", port = local.ports.osrm_car }
    osrm-bike = { profile = "bike", port = local.ports.osrm_bike }
  }
}

resource "aws_ecs_task_definition" "osrm" {
  for_each = local.osrm

  family                   = "dt-staging-${each.key}"
  requires_compatibilities = ["EC2"]
  network_mode             = "bridge"
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.task.arn # s3 read on the OSRM bucket

  container_definitions = jsonencode([
    {
      # Syncs s3://<bucket>/<version>/<profile>/ to the host once per
      # instance (a no-op when the files are there), then exits.
      name              = "fetch"
      image             = local.images.awscli
      essential         = false
      memoryReservation = local.init_memory.fetch.reservation
      memory            = local.init_memory.fetch.limit
      command = ["s3", "sync", "s3://${aws_s3_bucket.osrm.bucket}/${var.osrm_data_version}/${each.value.profile}/", "/data/",
      "--no-progress", "--only-show-errors"]
      environment      = [{ name = "AWS_REGION", value = var.region }]
      mountPoints      = [{ sourceVolume = "osrm-data", containerPath = "/data", readOnly = false }]
      linuxParameters  = { capabilities = { drop = ["ALL"] } }
      logConfiguration = { logDriver = "awslogs", options = merge(local.log_options, { awslogs-stream-prefix = "${each.key}-fetch" }) }
    },
    {
      name                   = "osrm"
      image                  = local.images.osrm
      essential              = true
      memoryReservation      = local.memory[each.key]
      memory                 = local.memory_limit[each.key]
      readonlyRootFilesystem = true
      command                = ["osrm-routed", "--algorithm", "mld", "--port", "5000", "/data/region.osrm"]
      portMappings           = [{ containerPort = 5000, hostPort = each.value.port, protocol = "tcp" }]
      mountPoints            = [{ sourceVolume = "osrm-data", containerPath = "/data", readOnly = true }]
      dependsOn              = [{ containerName = "fetch", condition = "SUCCESS" }]
      linuxParameters        = { capabilities = { drop = ["ALL"] } }
      # The image has no curl or wget: HTTP over bash's /dev/tcp (as in Compose).
      healthCheck = {
        command = ["CMD", "bash", "-c",
        "exec 3<>/dev/tcp/127.0.0.1/5000 && printf 'GET /nearest/v1/driving/4.9,52.37 HTTP/1.0\\r\\n\\r\\n' >&3 && head -n 1 <&3 | grep -q ' 200 '"]
        interval    = 10
        timeout     = 3
        retries     = 3
        startPeriod = 60
      }
      logConfiguration = { logDriver = "awslogs", options = merge(local.log_options, { awslogs-stream-prefix = each.key }) }
    },
  ])

  volume {
    name      = "osrm-data"
    host_path = "/data/osrm/${var.osrm_data_version}/${each.value.profile}"
  }
}

resource "aws_ecs_service" "infra" {
  for_each = {
    nats      = { td = aws_ecs_task_definition.nats.arn, count = 1 }
    redis     = { td = aws_ecs_task_definition.redis.arn, count = 1 }
    osrm-car  = { td = aws_ecs_task_definition.osrm["osrm-car"].arn, count = var.osrm_data_version == "" ? 0 : 1 }
    osrm-bike = { td = aws_ecs_task_definition.osrm["osrm-bike"].arn, count = var.osrm_data_version == "" ? 0 : 1 }
  }

  name            = each.key
  cluster         = aws_ecs_cluster.this.id
  task_definition = each.value.td
  desired_count   = each.value.count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.host.name
    weight            = 1
  }

  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  enable_ecs_managed_tags = false
  propagate_tags          = "NONE"

  depends_on = [aws_ecs_cluster_capacity_providers.this]
}

# ---- OSRM data bucket ---------------------------------------------------------------------

# Public map data (OpenStreetMap, ODbL), rebuilt by scripts/osrm-publish.sh.
# trivy:ignore:AVD-AWS-0089 Access logging is not needed for public map data.
# trivy:ignore:AVD-AWS-0090 Versioning is not needed: each publish writes a new date prefix and the data can be rebuilt.
resource "aws_s3_bucket" "osrm" {
  bucket        = var.osrm_bucket_name
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "osrm" {
  bucket                  = aws_s3_bucket.osrm.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "osrm" {
  bucket = aws_s3_bucket.osrm.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

# trivy:ignore:AVD-AWS-0132 OpenStreetMap-derived public data: S3-managed encryption is enough and keeps KMS out of the sync.
resource "aws_s3_bucket_server_side_encryption_configuration" "osrm" {
  bucket = aws_s3_bucket.osrm.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

data "aws_iam_policy_document" "osrm_bucket" {
  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.osrm.arn, "${aws_s3_bucket.osrm.arn}/*"]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "osrm" {
  bucket     = aws_s3_bucket.osrm.id
  policy     = data.aws_iam_policy_document.osrm_bucket.json
  depends_on = [aws_s3_bucket_public_access_block.osrm]
}
