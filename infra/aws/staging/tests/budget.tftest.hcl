# Offline guard (mock providers, plan only): the memory reserved by the
# long-running task definitions fits the host, and only the one-off migrate
# tasks carry the schema-owner URL.

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
mock_provider "random" {}
mock_provider "tls" {}

variables {
  staging_account_id = "123456789012"
  alert_email        = "ops@example.com"
}

run "reservations_fit_the_host" {
  command = plan

  assert {
    condition = (
      sum(flatten([for td in values(aws_ecs_task_definition.app) : [for c in jsondecode(td.container_definitions) : c.memoryReservation]])) +
      sum(flatten([for c in jsondecode(aws_ecs_task_definition.nats.container_definitions) : [c.memoryReservation]])) +
      sum(flatten([for c in jsondecode(aws_ecs_task_definition.redis.container_definitions) : [c.memoryReservation]])) +
      sum(flatten([for td in values(aws_ecs_task_definition.osrm) : [for c in jsondecode(td.container_definitions) : c.memoryReservation]]))
    ) <= 3400
    error_message = "long-running task reservations exceed 3.4 GB"
  }
}

run "no_schema_owner_url_on_services" {
  command = plan

  assert {
    condition = alltrue([
      for td in values(aws_ecs_task_definition.app) :
      !strcontains(td.container_definitions, "MIGRATIONS_DATABASE_URL")
    ])
    error_message = "a run task definition carries MIGRATIONS_DATABASE_URL"
  }

  assert {
    condition = alltrue([
      for td in values(aws_ecs_task_definition.migrate) :
      strcontains(td.container_definitions, "MIGRATIONS_DATABASE_URL")
    ])
    error_message = "a migrate task definition lacks MIGRATIONS_DATABASE_URL"
  }
}
