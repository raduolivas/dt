# Web CDN: one private S3 bucket behind one CloudFront distribution at
# cdn.<zone>. Prefixes:
#   page/<git sha>/          the hosted tracking page (deploy workflow)
#   sdk/tracking/<version>/  the tracking SDK (deploy workflow; version = git sha on staging)
#   tiles/nl.pmtiles         the Netherlands map tiles (owner: make tiles-upload)
# page/ and sdk/ are immutable (Cache-Control: public, max-age=31536000,
# immutable) and never deleted by a deploy, so an open page keeps loading
# its chunks after the next deploy. Only CloudFront reads the bucket (origin
# access control); there is no public access of any kind.

locals {
  cdn_bucket = "dt-staging-cdn-${var.staging_account_id}"
  cdn_url    = "https://${var.cdn_hostname}"

  # AWS managed policies (fixed IDs, the same in every account).
  cf_cache_policy_caching_optimized = "658327ea-f89d-4fab-a63d-7e88639e58f6"
  cf_origin_request_cors_s3_origin  = "88a5eaf4-2fd4-4709-b370-b4c650ea3fcf"
}

# ---- bucket -------------------------------------------------------------------------------

# Public web assets and OpenStreetMap-derived tiles, all rebuildable from git and OSM.
# trivy:ignore:AVD-AWS-0089 Access logging is not needed for public, rebuildable web assets (CloudFront is the only reader).
# trivy:ignore:AVD-AWS-0090 Versioning stays off: page/ and sdk/ are written once per git sha, tiles are rebuilt from OSM.
resource "aws_s3_bucket" "cdn" {
  bucket        = local.cdn_bucket
  force_destroy = true # make staging-down empties it; everything in it can be rebuilt
}

resource "aws_s3_bucket_public_access_block" "cdn" {
  bucket                  = aws_s3_bucket.cdn.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "cdn" {
  bucket = aws_s3_bucket.cdn.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_versioning" "cdn" {
  bucket = aws_s3_bucket.cdn.id

  versioning_configuration {
    status = "Disabled"
  }
}

# trivy:ignore:AVD-AWS-0132 Public web assets: S3-managed encryption is enough, and CloudFront's OAC reads SSE-S3 without a KMS grant.
resource "aws_s3_bucket_server_side_encryption_configuration" "cdn" {
  bucket = aws_s3_bucket.cdn.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# CORS at the origin as well, so a preflight (OPTIONS with Origin, forwarded
# by the CORS-S3Origin request policy) gets a 200 from S3; the response
# headers policy below sets the headers on every answer.
resource "aws_s3_bucket_cors_configuration" "cdn" {
  bucket = aws_s3_bucket.cdn.id

  cors_rule {
    allowed_methods = ["GET", "HEAD"]
    allowed_origins = ["*"]
    allowed_headers = ["Range", "If-Match", "If-None-Match"]
    expose_headers  = ["Content-Length", "Content-Range", "ETag", "Accept-Ranges"]
    max_age_seconds = 86400
  }
}

data "aws_iam_policy_document" "cdn_bucket" {
  statement {
    sid       = "CloudFrontReadsThroughOac"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.cdn.arn}/*"]

    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.cdn.arn]
    }
  }

  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.cdn.arn, "${aws_s3_bucket.cdn.arn}/*"]

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

resource "aws_s3_bucket_policy" "cdn" {
  bucket     = aws_s3_bucket.cdn.id
  policy     = data.aws_iam_policy_document.cdn_bucket.json
  depends_on = [aws_s3_bucket_public_access_block.cdn]
}

# ---- deploy role: uploads page/ and sdk/ only -----------------------------------------------

# The GitHub OIDC role dt-deploy-staging belongs to the bootstrap
# (infra/aws/bootstrap/oidc.tf, phase B); this stack adds a second inline
# policy to it because the bucket lives here. PutObject only (aws s3 cp,
# multipart included): no list, no delete, no read, and nothing under
# tiles/ (the owner uploads those with make tiles-upload).
resource "aws_iam_role_policy" "deploy_cdn" {
  name = "cdn-upload"
  role = "dt-deploy-staging"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid    = "UploadPageAndSdk"
      Effect = "Allow"
      Action = ["s3:PutObject"]
      Resource = [
        "arn:aws:s3:::${local.cdn_bucket}/page/*",
        "arn:aws:s3:::${local.cdn_bucket}/sdk/*",
      ]
    }]
  })
}

# ---- certificate (us-east-1, as CloudFront requires) ------------------------------------------

resource "aws_acm_certificate" "cdn" {
  provider          = aws.us_east_1
  domain_name       = var.cdn_hostname
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "cdn_validation" {
  for_each = {
    for o in aws_acm_certificate.cdn.domain_validation_options : o.domain_name => {
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

resource "aws_acm_certificate_validation" "cdn" {
  provider                = aws.us_east_1
  certificate_arn         = aws_acm_certificate.cdn.arn
  validation_record_fqdns = [for r in aws_route53_record.cdn_validation : r.fqdn]

  timeouts {
    create = "30m"
  }
}

# ---- CloudFront -----------------------------------------------------------------------------

resource "aws_cloudfront_origin_access_control" "cdn" {
  name                              = "dt-staging-cdn"
  description                       = "CloudFront reads the dt-staging CDN bucket"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

# The page is embedded and the SDK runs on partner origins: every answer
# allows any origin (no credentials) and cross-origin embedding (CORP), so
# pages with COEP can load the files too.
resource "aws_cloudfront_response_headers_policy" "cdn" {
  name    = "dt-staging-cdn"
  comment = "CORS * and CORP cross-origin for the DT web CDN"

  cors_config {
    access_control_allow_credentials = false
    origin_override                  = true
    access_control_max_age_sec       = 86400

    access_control_allow_origins {
      items = ["*"]
    }

    access_control_allow_methods {
      items = ["GET", "HEAD", "OPTIONS"]
    }

    access_control_allow_headers {
      items = ["Range", "If-Match", "If-None-Match"]
    }

    # PMTiles reads the archive with range requests and checks the ETag.
    access_control_expose_headers {
      items = ["Content-Length", "Content-Range", "ETag", "Accept-Ranges"]
    }
  }

  custom_headers_config {
    items {
      header   = "Cross-Origin-Resource-Policy"
      value    = "cross-origin"
      override = true
    }
  }

  security_headers_config {
    content_type_options {
      override = true
    }

    strict_transport_security {
      access_control_max_age_sec = 31536000
      include_subdomains         = false
      override                   = true
    }
  }
}

# trivy:ignore:AVD-AWS-0010 No access logs: public, rebuildable assets on staging; a log bucket and its storage are not worth the budget.
# trivy:ignore:AVD-AWS-0011 No WAF: the region-lock SCP does not exempt wafv2, and the CDN serves only static public files.
resource "aws_cloudfront_distribution" "cdn" {
  enabled         = true
  is_ipv6_enabled = true
  comment         = "DT staging web CDN (page, sdk, tiles)"
  aliases         = [var.cdn_hostname]
  http_version    = "http2and3"
  price_class     = "PriceClass_100" # Europe and North America

  origin {
    origin_id                = "s3-cdn"
    domain_name              = aws_s3_bucket.cdn.bucket_regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.cdn.id
  }

  # CloudFront passes Range requests through (and caches the ranges), which
  # PMTiles needs; the cache key is the path only (CachingOptimized).
  default_cache_behavior {
    target_origin_id           = "s3-cdn"
    allowed_methods            = ["GET", "HEAD", "OPTIONS"]
    cached_methods             = ["GET", "HEAD"]
    viewer_protocol_policy     = "redirect-to-https"
    compress                   = true
    cache_policy_id            = local.cf_cache_policy_caching_optimized
    origin_request_policy_id   = local.cf_origin_request_cors_s3_origin
    response_headers_policy_id = aws_cloudfront_response_headers_policy.cdn.id
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    acm_certificate_arn      = aws_acm_certificate_validation.cdn.certificate_arn
    ssl_support_method       = "sni-only"
    minimum_protocol_version = "TLSv1.2_2021"
  }
}

resource "aws_route53_record" "cdn" {
  zone_id = aws_route53_zone.staging.zone_id
  name    = var.cdn_hostname
  type    = "A"

  alias {
    name                   = aws_cloudfront_distribution.cdn.domain_name
    zone_id                = aws_cloudfront_distribution.cdn.hosted_zone_id
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "cdn_ipv6" {
  zone_id = aws_route53_zone.staging.zone_id
  name    = var.cdn_hostname
  type    = "AAAA"

  alias {
    name                   = aws_cloudfront_distribution.cdn.domain_name
    zone_id                = aws_cloudfront_distribution.cdn.hosted_zone_id
    evaluate_target_health = false
  }
}
