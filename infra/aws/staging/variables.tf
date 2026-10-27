variable "region" {
  description = "The only region DT runs in (the bootstrap SCP denies the rest)."
  type        = string
  default     = "eu-central-1"
}

variable "staging_account_id" {
  description = "Account id of dt-staging (bootstrap output staging_account_id)."
  type        = string

  validation {
    condition     = can(regex("^[0-9]{12}$", var.staging_account_id))
    error_message = "staging_account_id must be a 12-digit AWS account id."
  }
}

variable "alert_email" {
  description = "Receives the staging alarms (SNS email; confirm the subscription mail). Never committed: set it in terraform.tfvars."
  type        = string
  sensitive   = true
}

variable "zone_name" {
  description = "Hosted zone delegated from the registrar (runbook step 6)."
  type        = string
  default     = "staging.deliveryontrack.io"
}

variable "api_hostname" {
  description = "Public API hostname (ALB, ACM certificate)."
  type        = string
  default     = "api.staging.deliveryontrack.io"
}

variable "tracking_url_base" {
  description = "Base of tracking links (<base>/t/<token>); the hosted page arrives in M4."
  type        = string
  default     = "https://track.staging.deliveryontrack.io"
}

variable "vpc_cidr" {
  description = "VPC range; also the gateway's TRUSTED_PROXIES (the ALB's addresses)."
  type        = string
  default     = "10.40.0.0/16"
}

variable "instance_type" {
  description = "The single ECS host. Upgrade path: t3.large (+about EUR 35/month); tell the owner first."
  type        = string
  default     = "t3.medium"
}

variable "image_tag" {
  description = "Image tag the task definitions start with. Deploys register new revisions with the commit SHA; this only matters before the first deploy."
  type        = string
  default     = "bootstrap"
}

variable "osrm_bucket_name" {
  description = "Bucket with the preprocessed OSRM data (scripts/osrm-publish.sh). S3 names are global: override it if the name is taken."
  type        = string
  default     = "dt-staging-osrm"
}

variable "osrm_data_version" {
  description = "Date prefix (YYYY-MM-DD) of the OSRM data in the bucket, printed by scripts/osrm-publish.sh. Empty: the OSRM services stay at 0 tasks."
  type        = string
  default     = ""

  validation {
    condition     = var.osrm_data_version == "" || can(regex("^[0-9]{4}-[0-9]{2}-[0-9]{2}$", var.osrm_data_version))
    error_message = "osrm_data_version must be empty or a YYYY-MM-DD date."
  }
}

variable "docker_bridge_ip" {
  description = "Address of the host on Docker's default bridge (docker0) of the ECS-optimized AMI. Containers reach each other's static host ports through it."
  type        = string
  default     = "172.17.0.1"
}

variable "schedule_timezone" {
  description = "Time zone of the working-hours schedule."
  type        = string
  default     = "Europe/Amsterdam"
}

variable "schedule_enabled" {
  description = "Stop the host and the database outside 08:00-20:00 Monday-Friday."
  type        = bool
  default     = true
}
