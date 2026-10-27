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
  description = "Budget currency. If AWS Budgets rejects EUR for this payer, set USD in terraform.tfvars (see the runbook)."
  type        = string
  default     = "EUR"
}

variable "anomaly_threshold" {
  description = "Absolute cost anomaly impact (in the account's billing unit) that triggers an alert."
  type        = number
  default     = 10
}
