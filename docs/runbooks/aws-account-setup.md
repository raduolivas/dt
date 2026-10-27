# Runbook: AWS account setup (owner)

Who: the owner (steps need a person: card, phone, MFA device). Result: an AWS Organization with `dt-staging` and `dt-production`, guard rails, budgets and CI access, all from `infra/aws/bootstrap`. Total running cost of what this creates is about EUR 3-4/month (two KMS keys with rotation, a little S3 and CloudTrail storage); everything else is free.

Region: `eu-central-1` (Frankfurt). Budget: EUR 82/month before VAT across all accounts (about EUR 100 including VAT).

## 1. Create the AWS account (management account)  [cost: free]

1. Sign up at aws.amazon.com with a **dedicated email** you control (for example `aws@<your domain>`). Choose the Basic (free) support plan.
2. Sign in as root and enable **MFA on root** (hardware key preferred, TOTP otherwise).
3. Confirm there are **no root access keys** (Security credentials, Access keys: none).
4. Billing and Cost Management: set the **payment currency to EUR**, and turn on **IAM user and role access to Billing information** (Account settings).
5. Have three more unique emails ready (plus-addressing such as `you+dt-staging@example.com` works): one for `dt-staging`, one for `dt-production`, and the address that receives budget alerts.

## 2. Enable IAM Identity Center in eu-central-1  [cost: free]

1. Switch the console to **Europe (Frankfurt)**.
2. IAM Identity Center, Enable, and choose "Enable with AWS Organizations". This creates the Organization if there is none. That is fine: `make aws-bootstrap` detects an existing organization and imports it, so you do nothing extra.
3. Settings, Authentication, MFA: set "Prompt users for MFA" to **Every time they sign in**, with authenticator apps and security keys allowed, and "If a user does not yet have a registered MFA device" to **Require them to register an MFA device at sign-in**. **This setting is the control that enforces MFA for every portal session**; no policy in the stack does. Step 4 verifies it.

## 3. Phase A: bootstrap as root from CloudShell  [cost: about EUR 3-4/month afterwards]

Root is used here once, because no Identity Center user exists yet. Root cannot assume roles in other accounts, so this phase leaves out the GitHub OIDC role in `dt-staging`; phase B (step 4) adds it.

1. Open CloudShell (Frankfurt) signed in as root of the management account.
2. Install OpenTofu 1.13.2 (user-local, checksum verified):

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
4. `make aws-bootstrap PHASE=a`. Review the plan and type `yes`. The script:
   - imports the Organization if Identity Center already created it, and reuses AWS's default service cost anomaly monitor if there is one (AWS allows only one);
   - creates the OUs and the two accounts (a few minutes), SCPs, Identity Center permission sets, user and groups, CloudTrail, budgets and the state bucket, with local state;
   - migrates the state into `s3://dt-tofu-state-<management account id>/bootstrap/terraform.tfstate`.
   If it stops half way, fix the cause and run the same command again: it keeps and reuses the local state until the migration succeeded.
5. If Budgets rejects the EUR unit, set `budget_unit = "USD"` and `budget_limit = "82"` in `terraform.tfvars` and re-run. 82 USD is about EUR 76, slightly stricter than EUR 82, which is the safe side; do not raise it.
6. Delete the local state copy the script mentions, then sign out of root.
7. Confirm the budget alert subscription: AWS emails the alert address; accept it.

## 4. Sign in through Identity Center, then phase B  [cost: free]

1. Open the invitation email sent to `owner_email`, set a password and register an MFA device.
2. Sign in through the portal URL (Identity Center, Dashboard) with the `Admin` permission set on the **management** account. From now on this is the only way in; **never use root again** (the SCPs also block root in the member accounts).
3. Verify MFA is enforced: in Identity Center, Settings, Authentication, MFA still reads "Every time they sign in" with registration required. Sign out and back in once to confirm the portal asks for the MFA code.
4. Open CloudShell from this Admin session. It is a different home directory from root's: repeat step 3.2 (install tofu), then:

   ```sh
   git clone https://github.com/raduolivas/dt.git && cd dt
   cp infra/aws/bootstrap/terraform.tfvars.example infra/aws/bootstrap/terraform.tfvars
   vi infra/aws/bootstrap/terraform.tfvars   # the same values as in phase A
   make aws-bootstrap PHASE=b
   ```
   The script refuses to run phase B as root. It reads the state from the state bucket and adds the GitHub OIDC provider and `dt-deploy-staging` role in `dt-staging` (through `OrganizationAccountAccessRole`).
5. Record the outputs (account ids are not secret): `tofu -chdir=infra/aws/bootstrap output`.

   | Output | Value |
   |---|---|
   | management account id | `<RECORD: management account id>` |
   | `staging_account_id` | `<RECORD: staging account id>` |
   | `production_account_id` | `<RECORD: production account id>` |
   | `deploy_role_arn_staging` | `<RECORD: deploy role arn>` |
   | `state_bucket` | `<RECORD: dt-tofu-state-...>` |
6. Verify:
   - `aws budgets describe-budgets --account-id <management id>` lists `dt-monthly`;
   - from the staging account (portal, `Admin` on `dt-staging`), `aws ec2 describe-instances --region us-east-1` is **denied** by the SCP.

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
| KMS keys (CloudTrail, state), about EUR 1 each, plus about EUR 1 per key once automatic rotation has produced a second key version | about EUR 2/month, up to EUR 4 after the first rotation year |
| S3 (state, logs) | cents |
| **Total** | **about EUR 3-4/month** |

## Break glass

If the portal is unavailable, sign in as root with the MFA device, fix the problem, sign out. Record the reason here with the date.
