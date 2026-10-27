# Public ALB: TLS ends here (ACM), HTTP redirects to HTTPS. The viewer
# snapshot and streams go to tracking, everything else to the gateway.
# Idle timeout 120 s for the WebSocket and SSE streams; both send data at
# least every 25 s (the stream ping), well inside it.

# trivy:ignore:AVD-AWS-0053 The public API load balancer must face the internet.
resource "aws_lb" "api" {
  name               = "dt-staging-api"
  load_balancer_type = "application"
  internal           = false
  subnets            = aws_subnet.public[*].id
  security_groups    = [aws_security_group.alb.id]
  idle_timeout       = 120

  drop_invalid_header_fields = true
  enable_deletion_protection = false # make staging-down removes it
  desync_mitigation_mode     = "strictest"
}

resource "aws_lb_target_group" "gateway" {
  name                 = "dt-staging-gateway"
  port                 = local.ports.gateway
  protocol             = "HTTP"
  target_type          = "instance"
  vpc_id               = aws_vpc.this.id
  deregistration_delay = 15

  health_check {
    path                = "/readyz"
    matcher             = "200"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

resource "aws_lb_target_group" "tracking" {
  name                 = "dt-staging-tracking"
  port                 = local.ports.tracking
  protocol             = "HTTP"
  target_type          = "instance"
  vpc_id               = aws_vpc.this.id
  deregistration_delay = 15

  health_check {
    path                = "/readyz"
    matcher             = "200"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.api.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = aws_acm_certificate_validation.api.certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.gateway.arn
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.api.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "redirect"

    redirect {
      protocol    = "HTTPS"
      port        = "443"
      status_code = "HTTP_301"
    }
  }
}

resource "aws_lb_listener_rule" "tracking" {
  listener_arn = aws_lb_listener.https.arn
  priority     = 10

  condition {
    path_pattern {
      values = ["/v1/tracking/snapshot", "/v1/tracking/stream*"]
    }
  }

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.tracking.arn
  }
}
