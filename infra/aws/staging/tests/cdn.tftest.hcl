# Offline guard (mock providers, plan only) for the web CDN (cdn.tf): the
# bucket is private and reachable only through CloudFront's origin access
# control, the distribution answers on cdn.staging, the deploy role can only
# write page/* and sdk/*, track.staging reaches the gateway, and the gateway
# task carries the hosted page's settings.

mock_provider "aws" {
  # The provider validates ARNs at plan time; give every mocked ARN a valid shape.
  mock_resource "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::123456789012:role/mock"
    }
  }
  mock_resource "aws_sns_topic" {
    defaults = {
      arn = "arn:aws:sns:eu-central-1:123456789012:mock"
    }
  }
  mock_resource "aws_iam_instance_profile" {
    defaults = {
      arn = "arn:aws:iam::123456789012:instance-profile/mock"
    }
  }
  mock_resource "aws_lb" {
    defaults = {
      arn = "arn:aws:elasticloadbalancing:eu-central-1:123456789012:loadbalancer/app/mock/1"
    }
  }
  mock_resource "aws_lb_target_group" {
    defaults = {
      arn = "arn:aws:elasticloadbalancing:eu-central-1:123456789012:loadbalancer/app/mock/1"
    }
  }
  mock_resource "aws_acm_certificate" {
    defaults = {
      arn = "arn:aws:acm:eu-central-1:123456789012:certificate/mock"
    }
  }
  mock_resource "aws_db_instance" {
    defaults = {
      master_user_secret = [{ secret_arn = "arn:aws:secretsmanager:eu-central-1:123456789012:secret:mock", kms_key_id = "mock", secret_status = "active" }]
    }
  }
  mock_resource "aws_launch_template" {
    defaults = {
      id = "lt-0123456789abcdef0"
    }
  }
  mock_resource "aws_lb_listener" {
    defaults = {
      arn = "arn:aws:elasticloadbalancing:eu-central-1:123456789012:listener/app/mock/1/2"
    }
  }
  mock_resource "aws_autoscaling_group" {
    defaults = {
      arn = "arn:aws:autoscaling:eu-central-1:123456789012:autoScalingGroup:1:autoScalingGroupName/mock"
    }
  }
  mock_resource "aws_kms_key" {
    defaults = {
      arn = "arn:aws:kms:eu-central-1:123456789012:key/00000000-0000-0000-0000-000000000000"
    }
  }
  mock_resource "aws_cloudfront_distribution" {
    defaults = {
      arn            = "arn:aws:cloudfront::123456789012:distribution/EMOCK"
      domain_name    = "dmock.cloudfront.net"
      hosted_zone_id = "Z2FDTNDATAQYW2"
    }
  }
  mock_resource "aws_cloudfront_origin_access_control" {
    defaults = {
      id = "EMOCKOAC"
    }
  }
  mock_data "aws_availability_zones" {
    defaults = {
      names = ["eu-central-1a", "eu-central-1b", "eu-central-1c"]
    }
  }
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }
}

# The CloudFront certificate lives in us-east-1.
mock_provider "aws" {
  alias = "us_east_1"

  mock_resource "aws_acm_certificate" {
    defaults = {
      arn = "arn:aws:acm:us-east-1:123456789012:certificate/mock"
    }
  }
}

mock_provider "random" {}
mock_provider "tls" {}

variables {
  staging_account_id = "123456789012"
  alert_email        = "ops@example.com"
}

run "bucket_is_private" {
  command = plan

  assert {
    condition     = aws_s3_bucket.cdn.bucket == "dt-staging-cdn-123456789012"
    error_message = "the CDN bucket is not dt-staging-cdn-<account>"
  }

  assert {
    condition = (
      aws_s3_bucket_public_access_block.cdn.block_public_acls &&
      aws_s3_bucket_public_access_block.cdn.block_public_policy &&
      aws_s3_bucket_public_access_block.cdn.ignore_public_acls &&
      aws_s3_bucket_public_access_block.cdn.restrict_public_buckets
    )
    error_message = "the CDN bucket does not block every kind of public access"
  }

  assert {
    condition     = aws_s3_bucket_ownership_controls.cdn.rule[0].object_ownership == "BucketOwnerEnforced"
    error_message = "the CDN bucket allows ACLs"
  }

  assert {
    condition     = one([for r in aws_s3_bucket_server_side_encryption_configuration.cdn.rule : one(r.apply_server_side_encryption_by_default).sse_algorithm]) == "AES256"
    error_message = "the CDN bucket is not SSE-S3 encrypted"
  }

  assert {
    condition     = aws_s3_bucket_versioning.cdn.versioning_configuration[0].status == "Disabled"
    error_message = "versioning must stay off on the CDN bucket"
  }
}

run "distribution_uses_oac_and_the_cdn_alias" {
  command = plan

  assert {
    condition     = aws_cloudfront_distribution.cdn.aliases == toset(["cdn.staging.deliveryontrack.io"])
    error_message = "the distribution does not answer on cdn.staging.deliveryontrack.io only"
  }

  assert {
    condition = alltrue([
      for o in aws_cloudfront_distribution.cdn.origin :
      o.origin_access_control_id == "EMOCKOAC" && length(o.s3_origin_config) == 0
    ])
    error_message = "every origin must use the origin access control (no legacy OAI, no public origin)"
  }

  assert {
    condition = (
      aws_cloudfront_origin_access_control.cdn.origin_access_control_origin_type == "s3" &&
      aws_cloudfront_origin_access_control.cdn.signing_behavior == "always" &&
      aws_cloudfront_origin_access_control.cdn.signing_protocol == "sigv4"
    )
    error_message = "the origin access control must always sign S3 requests with SigV4"
  }

  assert {
    condition = (
      aws_cloudfront_distribution.cdn.http_version == "http2and3" &&
      aws_cloudfront_distribution.cdn.price_class == "PriceClass_100" &&
      one(aws_cloudfront_distribution.cdn.default_cache_behavior).compress &&
      one(aws_cloudfront_distribution.cdn.default_cache_behavior).viewer_protocol_policy == "redirect-to-https"
    )
    error_message = "the distribution must serve HTTP/2 and HTTP/3, compress, redirect to HTTPS and use price class 100"
  }

  assert {
    condition = (
      one(aws_cloudfront_distribution.cdn.viewer_certificate).ssl_support_method == "sni-only" &&
      one(aws_cloudfront_distribution.cdn.viewer_certificate).acm_certificate_arn == "arn:aws:acm:us-east-1:123456789012:certificate/mock"
    )
    error_message = "the distribution must use the us-east-1 ACM certificate over SNI"
  }

  assert {
    condition = (
      contains(one(one(aws_cloudfront_response_headers_policy.cdn.cors_config).access_control_allow_origins).items, "*") &&
      anytrue([
        for h in one(aws_cloudfront_response_headers_policy.cdn.custom_headers_config).items :
        h.header == "Cross-Origin-Resource-Policy" && h.value == "cross-origin"
      ])
    )
    error_message = "the response headers policy must add CORS * and Cross-Origin-Resource-Policy: cross-origin"
  }

  assert {
    condition     = aws_route53_record.cdn.name == "cdn.staging.deliveryontrack.io" && one(aws_route53_record.cdn.alias).name == "dmock.cloudfront.net"
    error_message = "cdn.staging must be an alias to the distribution"
  }
}

run "deploy_role_writes_only_page_and_sdk" {
  command = plan

  assert {
    condition     = aws_iam_role_policy.deploy_cdn.role == "dt-deploy-staging"
    error_message = "the CDN upload policy must be attached to the bootstrap's deploy role"
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_iam_role_policy.deploy_cdn.policy).Statement :
      s.Effect == "Allow" && toset(flatten([s.Action])) == toset(["s3:PutObject"])
    ])
    error_message = "the deploy role may only s3:PutObject on the CDN bucket"
  }

  assert {
    condition = toset(flatten([for s in jsondecode(aws_iam_role_policy.deploy_cdn.policy).Statement : s.Resource])) == toset([
      "arn:aws:s3:::dt-staging-cdn-123456789012/page/*",
      "arn:aws:s3:::dt-staging-cdn-123456789012/sdk/*",
    ])
    error_message = "the deploy role may only write under page/* and sdk/* (tiles are the owner's)"
  }
}

run "track_host_reaches_the_gateway" {
  command = plan

  assert {
    condition     = contains(aws_acm_certificate.api.subject_alternative_names, "track.staging.deliveryontrack.io")
    error_message = "the ALB certificate lacks track.staging.deliveryontrack.io"
  }

  assert {
    condition     = aws_route53_record.track.name == "track.staging.deliveryontrack.io" && one(aws_route53_record.track.alias).name == aws_lb.api.dns_name
    error_message = "track.staging must be an alias to the ALB (its default action is the gateway)"
  }

  assert {
    condition     = aws_acm_certificate.cdn.domain_name == "cdn.staging.deliveryontrack.io"
    error_message = "the CloudFront certificate must be for cdn.staging.deliveryontrack.io"
  }
}

run "gateway_has_the_page_settings" {
  command = plan

  variables {
    image_tag = "0123456789abcdef0123456789abcdef01234567"
  }

  assert {
    condition = (
      {
        for e in one([for c in jsondecode(aws_ecs_task_definition.app["gateway"].container_definitions) : c if c.name == "gateway"]).environment :
        e.name => e.value if startswith(e.name, "TRACKING_")
        } == {
        TRACKING_URL_BASE   = "https://track.staging.deliveryontrack.io"
        TRACKING_PAGE_BASE  = "https://cdn.staging.deliveryontrack.io/page/0123456789abcdef0123456789abcdef01234567/"
        TRACKING_PAGE_CDN   = "https://cdn.staging.deliveryontrack.io"
        TRACKING_PAGE_TILES = "https://cdn.staging.deliveryontrack.io/tiles/nl.pmtiles"
        TRACKING_PAGE_API   = "https://api.staging.deliveryontrack.io/v1"
        TRACKING_RPC_URL    = "https://tracking:8085"
      }
    )
    error_message = "the gateway task env does not carry the five TRACKING_* values of the hosted page"
  }
}
