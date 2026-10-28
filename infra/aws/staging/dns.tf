# The zone survives `make staging-down` (prevent_destroy): its name servers
# are delegated at the registrar once (runbook step 6) and must not change.
resource "aws_route53_zone" "staging" {
  name    = var.zone_name
  comment = "DT staging; delegated from the registrar"

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_acm_certificate" "api" {
  domain_name       = var.api_hostname
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "api_validation" {
  for_each = {
    for o in aws_acm_certificate.api.domain_validation_options : o.domain_name => {
      name   = o.resource_record_name
      type   = o.resource_record_type
      record = o.resource_record_value
    }
  }

  zone_id         = aws_route53_zone.staging.zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 300
  allow_overwrite = true
}

# Waits until ACM sees the validation record, so the zone must be delegated
# first (scripts/staging.sh up checks that before the full apply).
resource "aws_acm_certificate_validation" "api" {
  certificate_arn         = aws_acm_certificate.api.arn
  validation_record_fqdns = [for r in aws_route53_record.api_validation : r.fqdn]

  timeouts {
    create = "30m"
  }
}

resource "aws_route53_record" "api" {
  zone_id = aws_route53_zone.staging.zone_id
  name    = var.api_hostname
  type    = "A"

  alias {
    name                   = aws_lb.api.dns_name
    zone_id                = aws_lb.api.zone_id
    evaluate_target_health = false
  }
}
