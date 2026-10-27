# Runbook: AWS account setup (owner)

Who: the owner (steps need a person: card, phone, MFA device). Result: an AWS Organization with `dt-staging` and `dt-production`, guard rails, budgets and CI access, all from `infra/aws/bootstrap`. Total running cost of what this creates is about EUR 3/month (two KMS keys, a little S3, CloudTrail storage); everything else is free tier or free.

Region: `eu-central-1` (Frankfurt). Budget: EUR 82/month before VAT across all accounts (about EUR 100 including VAT).

## 1. Create the AWS account (management account)  [cost: free]

1. Sign up at aws.amazon.com with a **dedicated email** you control (for example `aws@<your domain>`). Choose the Basic (free) support plan.
2. Sign in as root and enable **MFA on root** (hardware key preferred, TOTP otherwise).
3. Confirm there are **no root access keys** (Security credentials, Access keys: none).
4. Billing and Cost Management: set the **payment currency to EUR**, and turn on **IAM user and role access to Billing information** (Account settings).
5. Have three more unique emails ready (plus-addressing such as `you+dt-staging@example.com` works): one for `dt-staging`, one for `dt-production`, and the address that receives budget alerts.

## 2. Enable IAM Identity Center in eu-central-1  [cost: free]

1. Switch the console to **Europe (Frankfurt)**.
2. IAM Identity Center, Enable. Choose "Enable with AWS Organizations" if offered; if no organization exists yet, accept creating one (the stack adopts it: if `tofu apply` later says the organization already exists, run `tofu import aws_organizations_organization.this <org id>` first).
3. Settings, Authentication, MFA: require MFA **every time users sign in**.

## 3. Run the bootstrap once from CloudShell  [cost: about EUR 3/month afterwards]

1. Open CloudShell (Frankfurt) as root or as an administrator of the management account. This is the one time a person acts outside the portal.
2. Install OpenTofu 1.13.2 in CloudShell (user-local, verify the checksum from the release page):

   ```sh
   V=1.13.2
   curl -sLO https://github.com/opentofu/opentofu/releases/download/v$V/tofu_${V}_linux_amd64.tar.gz
   curl -sLO https://github.com/opentofu/opentofu/releases/download/v$V/tofu_${V}_SHA256SUMS
   sha256sum --ignore-missing -c tofu_${V}_SHA256SUMS
   mkdir -p ~/bin && tar xzf tofu_${V}_linux_amd64.tar.gz -C ~/bin tofu
   ```
   (On an ARM CloudShell use `linux_arm64`.)
3. Clone the repository and create the variables file:

   ```sh
   git clone https://github.com/raduolivas/dt.git && cd dt
   cp infra/aws/bootstrap/terraform.tfvars.example infra/aws/bootstrap/terraform.tfvars
   vi infra/aws/bootstrap/terraform.tfvars   # real emails; the file is git-ignored
   ```
4. `make aws-bootstrap`. The script:
   - creates the Organization, OUs and the two accounts first (account creation takes a few minutes);
   - then applies the rest: SCPs, Identity Center users and permission sets, CloudTrail, budgets, GitHub OIDC role, state bucket;
   - then migrates its state into `s3://dt-tofu-state-<management account id>/bootstrap/terraform.tfstate` and tells you to delete the local state files.
   Review the plan before typing `yes` each time.
5. If Budgets rejects the EUR unit, set `budget_unit = "USD"` in `terraform.tfvars` and re-run.
6. Record the outputs in the table below (account ids are not secret): `tofu -chdir=infra/aws/bootstrap output`.
7. Confirm the budget alert email subscription: AWS sends a confirmation message to the alert address; accept it.

| Output | Value |
|---|---|
| management account id | `<RECORD: management account id>` |
| `staging_account_id` | `<RECORD: staging account id>` |
| `production_account_id` | `<RECORD: production account id>` |
| `deploy_role_arn_staging` | `<RECORD: deploy role arn>` |
| `state_bucket` | `<RECORD: dt-tofu-state-...>` |

## 4. Sign in through Identity Center from now on  [cost: free]

1. Check the invitation email sent to `owner_email`, set a password and register an MFA device.
2. Use the portal URL (Identity Center, Dashboard) for every future login, with the `Admin` permission set. **Never use root again** (root stays locked behind MFA; the SCPs also block root in the member accounts).
3. Verify:
   - `aws budgets describe-budgets --account-id <management id>` lists `dt-monthly`;
   - from the staging account, `aws ec2 describe-instances --region us-east-1` is **denied** by the SCP.

## 5. GitHub environment `staging`  [cost: free]

Repository Settings, Environments, New environment `staging`, with **Required reviewers = the owner**. The deploy role trusts only `repo:raduolivas/dt:environment:staging`, so deploys cannot run without this environment. Add `iac` to the required status checks of `main`.

## 6. Delegate DNS for staging  [cost: Route 53 hosted zone about EUR 0.50/month, created in Task 14]

After Task 14 creates the Route 53 zone for `staging.deliveryontrack.io`, create four **NS records** for `staging` at the registrar pointing to the zone's name servers (from the Task 14 output `name_servers`). Wait for propagation before the first deploy; the certificate validation depends on it.

## Costs summary

| Item | Cost |
|---|---|
| Account, Organizations, Identity Center, SCPs, IAM, OIDC | free |
| CloudTrail (first management-events trail), Cost Anomaly Detection | free |
| Budgets (first two free; this stack uses one) | free |
| KMS keys (CloudTrail, state), about EUR 1 each | about EUR 2/month |
| S3 (state, logs) | cents |

## Break glass

If the portal is unavailable, sign in as root with the MFA device, fix the problem, sign out. Record the reason here with the date.
