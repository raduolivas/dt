# infra/aws/bootstrap

OpenTofu stack that sets up the AWS guard rails once, from the management account. Design: `docs/superpowers/specs/2026-10-26-m3-tracking-design.md` section 7. Owner steps: `docs/runbooks/aws-account-setup.md`.

## What it creates

| File | Resources |
|---|---|
| `organization.tf` | Organization (all features, SCPs on), OUs `Workloads`, `Workloads/Staging`, `Workloads/Production`, accounts `dt-staging` and `dt-production` (`prevent_destroy`, never closed by tofu) |
| `scp.tf` | On the `Workloads` OU only (SCPs never bind the management account): `dt-region-lock` (deny outside `eu-central-1` with `NotAction` for `iam`, `organizations`, `route53`, `route53domains`, `cloudfront`, `budgets`, `ce`, `sts`, `support`, `acm`; `acm` is also allowed in `us-east-1`) and `dt-guard-rails` (deny `LeaveOrganization`, `cloudtrail:StopLogging/DeleteTrail/UpdateTrail`, and every action by `arn:aws:iam::*:root`) |
| `identity_center.tf` | Permission sets `Admin` (plus an MFA deny policy) and `ReadOnly`, user `owner` (from `owner_email`), groups `dt-admins` and `dt-readonly`, assignments on all three accounts |
| `cloudtrail.tf` | Organization trail `dt-org-trail`; bucket `dt-cloudtrail-<mgmt id>` (versioned, SSE-KMS with a rotating key, TLS-only, public access blocked, 1-year lifecycle) |
| `budgets.tf` | Budget `dt-monthly` (82, alerts at 50/80/100% actual and 100% forecast) and a Cost Anomaly Detection monitor with a 10 threshold |
| `oidc.tf` | In `dt-staging`: GitHub OIDC provider and role `dt-deploy-staging` |
| `state.tf` | Bucket `dt-tofu-state-<mgmt id>` (versioned, SSE-KMS, TLS-only, public access blocked, native locking via `use_lockfile`) |

## Outputs Task 14 consumes

`staging_account_id`, `production_account_id`, `deploy_role_arn_staging`, `state_bucket`, `state_kms_key_arn`, `staging_task_role_names`.

## Contract with `infra/aws/staging`

The deploy role `dt-deploy-staging` (trust: `repo:raduolivas/dt:environment:staging`, audience `sts.amazonaws.com`) can do only this, and only on these names. The staging stack must create resources with exactly these names:

- ECS cluster `dt-staging` (services, tasks and `dt-staging-*` task definitions inside it).
- ECR repositories `dt-*`.
- IAM roles `dt-staging-task-execution` and `dt-staging-task` (the only roles it may `iam:PassRole`, and only to `ecs-tasks.amazonaws.com`).

Allowed actions: ECR login and push; `ecs:UpdateService`/`DescribeServices` on the cluster's services; `ecs:RunTask` (migrations) for `dt-staging-*` task definitions in that cluster; `DescribeTasks`/`StopTask`; `Register`/`Describe`/`DeregisterTaskDefinition`. Nothing else. If a deploy needs more, change `oidc.tf` here.

## Variables

`owner_email`, `alert_email`, `staging_account_email`, `production_account_email` are required and sensitive. Put them in `terraform.tfvars` (git-ignored, never committed); copy `terraform.tfvars.example`.

## Running it

Owners run `make aws-bootstrap` from CloudShell (runbook step 3). The script creates the accounts first (the staging provider needs the account id), applies the rest with local state, then migrates the state into the state bucket by writing the git-ignored `backend_override.tf`. Re-running it afterwards applies against the remote state.

Local checks without credentials:

```sh
cd infra/aws/bootstrap
tofu init -backend=false && tofu validate && tofu fmt -check
trivy config --severity HIGH,CRITICAL infra/aws
```

Pinned: OpenTofu 1.13.2 (also in CI), AWS provider `~> 6.70.0` (`.terraform.lock.hcl` committed for linux amd64/arm64 and macOS arm64).

## Known limits

- MFA at sign-in is switched on in the Identity Center console (Settings, Authentication). The provider cannot set it. The `Admin` inline policy is a second layer (`BoolIfExists`, so it never locks the owner out when the key is absent).
- The management account is not covered by the SCPs; protect it with root MFA and by using the portal only.
- Budget amounts are in the unit set by `budget_unit` (EUR by default). Cost Anomaly Detection thresholds use the account's billing unit.
