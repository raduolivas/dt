variable "region" {
  description = "The only region DT runs in (the SCP denies the rest)."
  type        = string
  default     = "eu-central-1"
}

variable "owner_email" {
  description = "Email of the owner's Identity Center user. Never committed: set it in terraform.tfvars (git-ignored)."
  type        = string
  sensitive   = true
}

variable "owner_given_name" {
  description = "Given name of the owner's Identity Center user."
  type        = string
  default     = "DT"
}

variable "owner_family_name" {
  description = "Family name of the owner's Identity Center user."
  type        = string
  default     = "Owner"
}

variable "alert_email" {
  description = "Receives budget alerts and cost anomaly alerts. Never committed."
  type        = string
  sensitive   = true
}

variable "staging_account_email" {
  description = "Unique root email for the dt-staging account (plus-addressing works, e.g. you+dt-staging@example.com). Never committed."
  type        = string
  sensitive   = true
}

variable "production_account_email" {
  description = "Unique root email for the dt-production account. Never committed."
  type        = string
  sensitive   = true
}

variable "github_repository" {
  description = "GitHub repository (owner/name) whose staging environment may deploy."
  type        = string
  default     = "raduolivas/dt"
}

variable "budget_limit" {
  description = "Monthly budget across all accounts, before VAT (spec section 7.1: about EUR 100 including VAT)."
  type        = string
  default     = "82"
}

variable "budget_unit" {
  description = "Budget currency. If AWS Budgets rejects EUR for this payer, set budget_unit to USD and budget_limit to 82 in terraform.tfvars (82 USD is about EUR 76: slightly stricter, which is the safe side; see the runbook)."
  type        = string
  default     = "EUR"
}

variable "enable_staging_oidc" {
  description = "Create the GitHub OIDC provider and deploy role in dt-staging. Needs sts:AssumeRole, which root credentials cannot do, so phase A of scripts/aws-bootstrap.sh sets this to false and phase B (an Identity Center Admin session) leaves it true."
  type        = bool
  default     = true
}

variable "anomaly_monitor_arn" {
  description = "ARN of an existing DIMENSIONAL/SERVICE cost anomaly monitor to reuse (AWS allows one per account and may have created a default one). Null creates dt-services. Set by scripts/aws-bootstrap.sh."
  type        = string
  default     = null
}

variable "anomaly_threshold" {
  description = "Absolute cost anomaly impact in USD (Cost Explorer reports anomalies in USD whatever the budget unit) that triggers an alert."
  type        = number
  default     = 10
}
