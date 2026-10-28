# Two AZs, public subnets for the ALB and the host, private subnets for RDS.
# No NAT gateway: the host has a public IP and egresses directly; S3 (OSRM
# data, ECR layers) goes through a free gateway endpoint.

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, 2)

  # Static host ports (bridge mode): containers reach each other at
  # <name>:<port>, where every name resolves to the host's docker0 address.
  ports = {
    gateway      = 8080
    deliveries   = 8081
    tracking     = 8082
    webhooks     = 8083
    accounts     = 8084
    tracking_rpc = 8085
    nats         = 4222
    redis        = 6379
    osrm_car     = 5000
    osrm_bike    = 5001
  }
}

# trivy:ignore:AVD-AWS-0178 Flow logs would cost CloudWatch ingestion every hour; staging carries only simulated data.
resource "aws_vpc" "this" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = { Name = "dt-staging" }
}

# Locks the default security group down (no rules at all).
resource "aws_default_security_group" "this" {
  vpc_id = aws_vpc.this.id
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id

  tags = { Name = "dt-staging" }
}

resource "aws_subnet" "public" {
  count = 2

  vpc_id            = aws_vpc.this.id
  availability_zone = local.azs[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 8, count.index) # 10.40.0.0/24, 10.40.1.0/24
  # The launch template gives the host its public IP; nothing else gets one.
  map_public_ip_on_launch = false

  tags = { Name = "dt-staging-public-${local.azs[count.index]}" }
}

resource "aws_subnet" "db" {
  count = 2

  vpc_id            = aws_vpc.this.id
  availability_zone = local.azs[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 8, 10 + count.index) # 10.40.10.0/24, 10.40.11.0/24

  tags = { Name = "dt-staging-db-${local.azs[count.index]}" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }

  tags = { Name = "dt-staging-public" }
}

resource "aws_route_table_association" "public" {
  count = 2

  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# The DB subnets have no route out of the VPC at all.
resource "aws_route_table" "db" {
  vpc_id = aws_vpc.this.id

  tags = { Name = "dt-staging-db" }
}

resource "aws_route_table_association" "db" {
  count = 2

  subnet_id      = aws_subnet.db[count.index].id
  route_table_id = aws_route_table.db.id
}

resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.this.id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [aws_route_table.public.id]

  tags = { Name = "dt-staging-s3" }
}

# ---- security groups ----------------------------------------------------------

resource "aws_security_group" "alb" {
  name        = "dt-staging-alb"
  description = "Public ALB: HTTPS (and the HTTP redirect) from the internet"
  vpc_id      = aws_vpc.this.id
}

resource "aws_security_group" "host" {
  name        = "dt-staging-host"
  description = "ECS host: only the ALB on the public service ports"
  vpc_id      = aws_vpc.this.id
}

resource "aws_security_group" "rds" {
  name        = "dt-staging-rds"
  description = "RDS: PostgreSQL only from the ECS host"
  vpc_id      = aws_vpc.this.id
}

# The public API endpoint: HTTPS from anywhere is the point.
resource "aws_vpc_security_group_ingress_rule" "alb_https" {
  security_group_id = aws_security_group.alb.id
  description       = "HTTPS from the internet"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}

# Port 80 only answers with a redirect to HTTPS.
resource "aws_vpc_security_group_ingress_rule" "alb_http" {
  security_group_id = aws_security_group.alb.id
  description       = "HTTP from the internet (redirected to HTTPS)"
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "alb_to_host" {
  for_each = { gateway = local.ports.gateway, tracking = local.ports.tracking }

  security_group_id            = aws_security_group.alb.id
  description                  = "ALB to the ${each.key} target"
  ip_protocol                  = "tcp"
  from_port                    = each.value
  to_port                      = each.value
  referenced_security_group_id = aws_security_group.host.id
}

resource "aws_vpc_security_group_ingress_rule" "host_from_alb" {
  for_each = { gateway = local.ports.gateway, tracking = local.ports.tracking }

  security_group_id            = aws_security_group.host.id
  description                  = "${each.key} from the ALB"
  ip_protocol                  = "tcp"
  from_port                    = each.value
  to_port                      = each.value
  referenced_security_group_id = aws_security_group.alb.id
}

# trivy:ignore:AVD-AWS-0104 No NAT gateway by design: the host pulls images (ECR, Docker Hub, public ECR) and talks to SSM, ECS, Logs and Secrets Manager over public HTTPS endpoints. Only 443 is open.
resource "aws_vpc_security_group_egress_rule" "host_https" {
  security_group_id = aws_security_group.host.id
  description       = "HTTPS to AWS APIs and image registries"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "host_to_rds" {
  security_group_id            = aws_security_group.host.id
  description                  = "PostgreSQL"
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
  referenced_security_group_id = aws_security_group.rds.id
}

resource "aws_vpc_security_group_ingress_rule" "rds_from_host" {
  security_group_id            = aws_security_group.rds.id
  description                  = "PostgreSQL from the ECS host"
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
  referenced_security_group_id = aws_security_group.host.id
}
