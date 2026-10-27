# Runbook: AWS staging (owner)

Who: the owner, from a **management-account Identity Center Admin session** (CloudShell in `eu-central-1`, or a local profile). Never as root. Result: `https://api.staging.deliveryontrack.io` running the five services from `infra/aws/staging`, redeployed by `.github/workflows/deploy-staging.yml` on every merge to `main`.

Prerequisites: `docs/runbooks/aws-account-setup.md` steps 1-5 are done (bootstrap phases A and B, GitHub environment `staging`). Tools: `tofu` 1.13.2, `aws` CLI v2, `jq`, `curl`, Go (for `dtctl`), Docker (only for `scripts/osrm-publish.sh`), and the Session Manager plugin for the AWS CLI (step 10).

## Setup

### 1. Read the bootstrap outputs

In `infra/aws/bootstrap` (where phase B ran): `tofu output`. Keep `staging_account_id`, `deploy_role_arn_staging`, `state_bucket`, `state_kms_key_arn`. `scripts/staging.sh` reads `state_bucket` and `state_kms_key_arn` itself when the bootstrap directory has its state configured; elsewhere export them:

```sh
export DT_STATE_BUCKET=dt-tofu-state-<management account id>
export DT_STATE_KMS_KEY_ARN=<state_kms_key_arn>
```

### 2. Fill in the variables

```sh
cp infra/aws/staging/terraform.tfvars.example infra/aws/staging/terraform.tfvars
```

Set `staging_account_id` and `alert_email`. Leave `osrm_data_version` empty for now. The file is git-ignored.

### 3. GitHub settings

In the `staging` environment (aws-account-setup step 5): add the **variable** `AWS_DEPLOY_ROLE_ARN` = `deploy_role_arn_staging`, and under Deployment branches allow only `main`. The repository **secret** `STAGING_TEST_KEY` comes in step 10.

### 4. First `make staging-up`: the hosted zone

```sh
make staging-up
```

It applies only the hosted zone `staging.deliveryontrack.io`, checks the public delegation, finds none, prints the four name servers and stops (exit 3).

### 5. Note the name servers

The four `ns-*.awsdns-*` names printed in step 4 (`make staging-up` prints them again on every run until the delegation is visible; after the full apply they are also the output `zone_name_servers`).

### 6. Delegate DNS

At the registrar of `deliveryontrack.io`, create NS records for `staging` with those four name servers. Wait until `dig +short NS staging.deliveryontrack.io` (or `https://dns.google/resolve?name=staging.deliveryontrack.io&type=NS`) returns them.

### 7. Second `make staging-up`: everything

```sh
make staging-up
```

About 15-20 minutes (RDS is the slowest; the ACM validation needs step 6). Confirm the SNS subscription email sent to `alert_email`. The services cannot start yet: their images do not exist until the first deploy (step 9).

### 8. OSRM data

```sh
scripts/osrm-publish.sh
```

It builds the Noord-Holland car and bike data (MLD) with `scripts/osrm-prepare.sh noord-holland` and uploads it to `s3://dt-staging-osrm/<today>/`. Set the printed date as `osrm_data_version` in `terraform.tfvars` and run `make staging-up` again: the OSRM services start (each one syncs its files to the host once per instance). Refresh the map the same way whenever needed; the date pins the data, so an upload never changes a running deployment. It overwrites the local `infra/compose/osrm/` data; the next `make dev` rebuilds Amsterdam.

### 9. First deploy

Merge to `main` (or run the `deploy-staging` workflow from `main` under Actions). It needs the host awake (08:00-20:00 Monday-Friday, or `make staging-wake`). The smoke step fails until step 10 is done; that first failure is expected.

### 10. Staging test key (dtctl over SSM)

The accounts API is internal (mTLS on the host, port 8084). Reach it through a Session Manager port forward, with the dtctl certificate from Secrets Manager. Use credentials for `dt-staging` (portal: `Admin` on `dt-staging`, or `source scripts/lib/aws-staging.sh && use_staging_credentials` from a management session).

Terminal 1:

```sh
id="$(aws ec2 describe-instances --region eu-central-1 \
  --filters Name=tag:aws:autoscaling:groupName,Values=dt-staging-ecs-host Name=instance-state-name,Values=running \
  --query 'Reservations[0].Instances[0].InstanceId' --output text)"
aws ssm start-session --region eu-central-1 --target "$id" \
  --document-name AWS-StartPortForwardingSession --parameters '{"portNumber":["8084"],"localPortNumber":["18084"]}'
```

Terminal 2, from the repository root (nothing is echoed; the material lives in a private temp directory):

```sh
make dtctl
d="$(mktemp -d)"; chmod 700 "$d"
mtls="$(aws secretsmanager get-secret-value --region eu-central-1 --secret-id dt-staging/mtls --query SecretString --output text)"
jq -r .dtctl_pem <<<"$mtls" >"$d/dtctl.pem"; jq -r .dtctl_key <<<"$mtls" >"$d/dtctl.key"; jq -r .ca_pem <<<"$mtls" >"$d/ca.pem"; unset mtls
export DT_INTERNAL_RPC_TOKEN="$(aws secretsmanager get-secret-value --region eu-central-1 --secret-id dt-staging/app \
  --query SecretString --output text | jq -r .internal_rpc_token)"
export DT_ACCOUNTS_URL=https://localhost:18084 DT_INTERNAL_RPC_TLS_CERT="$d/dtctl.pem" \
  DT_INTERNAL_RPC_TLS_KEY="$d/dtctl.key" DT_INTERNAL_RPC_TLS_CA="$d/ca.pem"
tenant="$(backend/bin/dtctl tenant create --name "Staging smoke")"
backend/bin/dtctl tenant settings --tenant "$tenant" --models direct,end_to_end --default end_to_end --pickup any --prep-minutes 15
backend/bin/dtctl key create --tenant "$tenant" --env test --name staging-smoke | gh secret set STAGING_TEST_KEY --repo raduolivas/dt
rm -rf "$d"; unset DT_INTERNAL_RPC_TOKEN
```

The tenant settings are what `scripts/smoke-m3.sh` expects when it runs with `DT_SMOKE_KEY` (end_to_end by default, 15 preparation minutes). Re-run the deploy workflow; the smoke step must pass.

### 11. Verify (plan Task 14 step 5)

- `curl -fsS https://api.staging.deliveryontrack.io/livez` answers 200.
- The `deploy-staging` run is green, smoke included.
- Latency, with a test key of a tenant set up like step 10 (`--models direct,end_to_end --default end_to_end --pickup any`):
  `cd backend/tools/latency && DT_API_KEY=<key> go run . -api https://api.staging.deliveryontrack.io -stream https://api.staging.deliveryontrack.io -deliveries 20 -viewers 5 -duration 2m` reports p95 < 1000 ms.
- After the first full day, Cost Explorer's month-end forecast stays under the budget (about EUR 82 before VAT).

## Operating staging

### Working hours

EventBridge Scheduler (`Europe/Amsterdam`) starts the database at 07:50 and scales the host's Auto Scaling group to 1 at 08:00, and stops both at 20:00, Monday to Friday. At weekends it stays off (RDS restarts a stopped instance by itself only after 7 days, which a weekend never reaches).

- `make staging-wake`: start now (database about 5-10 minutes, then the services). It stays up until the next 20:00 stop or `make staging-sleep`.
- `make staging-sleep`: stop now.
- Out of hours cost about EUR 0.07 per hour awake (host, database, IP, disk).
- To keep it up all the time set `schedule_enabled = false` and `make staging-up`; that costs about EUR 30-35/month more and goes over the budget (see the cost table).

What the schedule implies:

- **The host is a new instance every working day** (the ASG terminates it at night). NATS JetStream (host volume `/data/nats`) and Redis start empty each morning; the services recreate their streams and consumers. Postgres keeps everything. The OSRM files are synced again from S3 at each start (free through the S3 gateway endpoint, about a minute).
- **The nightly retention jobs never run on staging.** They run in-process at 02:00 UTC under leases (Task 11), when staging is asleep. Staging holds only simulated data, so this is acceptable. To exercise them, run `make staging-wake` before 02:00 UTC (03:00/04:00 Amsterdam) and `make staging-sleep` after, or set `schedule_enabled = false` for a night.
- A deploy while staging is asleep pushes and signs the images and points the services at the new revisions, but skips the migrations, the stability wait and the smoke test (with a warning in the run). In the morning the services migrate at startup (they keep `MIGRATIONS_DATABASE_URL`). Re-run the workflow during working hours to get the smoke test.
- The alarms treat missing data as OK, so a sleeping staging does not alarm.

### Deploys

`.github/workflows/deploy-staging.yml` runs after the `ci` workflow succeeds on a push to `main`, in the `staging` environment, with the OIDC role `dt-deploy-staging` (no AWS keys anywhere):

1. Builds `linux/amd64` images, pushes them to ECR as `dt-<svc>:<commit sha>` (immutable tags, scan on push) and signs them with cosign keyless (GitHub OIDC, Rekor); the signature is verified before going on. A re-run skips images that exist and only signs what is unsigned.
2. Runs `dt-staging-db-bootstrap` (creates roles and schemas on a fresh database, sets the role passwords; idempotent), then `dt-staging-migrate-<svc>` for each service (`MIGRATE_ONLY=true`, as `mig_<svc>`), each waited for and required to exit 0.
3. Registers new task definition revisions (the latest revision of each family with only the image changed) and updates the services. Deployment circuit breaker with rollback is on.
4. Waits until the services are stable and checks that each one runs the new revision (a rollback fails the job), then `GET /livez`.
5. Runs `scripts/smoke-m3.sh` against `https://api.staging.deliveryontrack.io` with `DT_SMOKE_KEY` = `STAGING_TEST_KEY` (public API only, no dtctl).

With one host and static ports, a rollout stops the old task before starting the new one (minimum healthy 0 %): each service is unavailable for about 10-30 seconds per deploy. That is accepted for staging.

Check a signature by hand:

```sh
cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/raduolivas/dt/\.github/workflows/deploy-staging\.yml@refs/heads/main$' \
  <account>.dkr.ecr.eu-central-1.amazonaws.com/dt-gateway@sha256:<digest>
```

### Rollback

Re-run the `deploy-staging` workflow of the last good commit (Actions, the run, Re-run all jobs): its images are still in ECR (the last 15 per repository are kept), so it only re-registers and updates. Or in the console: ECS, cluster `dt-staging`, service, Update, previous revision.

### Changing infrastructure

Edit `infra/aws/staging`, open a PR (CI validates and scans it), merge, then `make staging-up`. Environment variable changes reach the services at the next deploy (the services ignore task definition changes made by tofu; the deploy copies the latest revision).

### Secrets

All values are generated by tofu (`random_bytes`, `tls`) and stored in Secrets Manager under `alias/dt-staging-data` (`dt-staging/app`, `dt-staging/db`, `dt-staging/mtls`, plus the RDS-managed master secret). They are never printed: no output carries one. To rotate one, `tofu -chdir=infra/aws/staging apply -replace='random_bytes.<name>'` through a management Admin session (after `make staging-up` has initialised the backend), then re-run the deploy. Never `-replace` the data keys (`gateway_data_key`, `deliveries_data_key`): sealed data would become unreadable. Rotating one follows `docs/runbooks/data-key-rotation.md`: add a second `random_bytes` and a `gw2:`/`dk2:` entry next to the old one in `secrets.tf`, deploy, switch `DATA_KEY_CURRENT`, and drop the old key only after re-sealing. The mTLS certificates last 825 days and renew at the first apply in their last 60 days.

### Logs and alarms

CloudWatch log group `/dt/staging` (14 days), one stream prefix per container. Alarms by email (SNS `dt-staging-alerts`): host CPU > 80 % for 10 minutes, host memory > 90 % (CloudWatch agent), ALB 5xx > 5 in 5 minutes, RDS free storage < 2 GB. The budget alerts come from the bootstrap. Shell on the host: `aws ssm start-session --target <instance id>` (no SSH, no key pair).

## Teardown

```sh
make staging-down
```

Destroys everything except the hosted zone (delegation stays valid), the KMS key and its alias, and the state (bootstrap). It asks for confirmation. The database goes without a final snapshot (staging holds only simulated data); secrets are deleted without a recovery window so a later `make staging-up` can recreate them at once with new values. To come back: `make staging-up`, `scripts/osrm-publish.sh` (the bucket was emptied), `make staging-up`, run the deploy, and redo step 10 (new database, new key).

The KMS key costs EUR 1/month while kept. To remove it and the zone for good, delete their `prevent_destroy` blocks in a reviewed PR first.

## Cost estimate

Prices: AWS on-demand list prices for `eu-central-1` in USD. Fetched on 2026-10-27 from the public AWS price files: EC2 `t3.medium` (published 2026-10-27), RDS PostgreSQL `db.t4g.micro` and gp3 storage (published 2026-10-26), ALB hours and LCUs (`AWSELB` offer, published 2026-09-11), public IPv4 (`AmazonVPC` offer), EBS gp3, Secrets Manager. KMS, Route 53, CloudWatch, ECR and S3 are the long-standing list prices, not re-fetched. Exchange rate assumed: 1 USD = EUR 0.86 (and a pessimistic EUR 0.95 for the range). All before VAT.

Assumptions: 21.75 weekdays per month; host up 12 h per weekday (261 h), database 12 h 10 min (265 h); 2 GB of logs per month; ALB traffic at test level (about 0.1 LCU on average, rounded up to USD 1); data transfer out within the 100 GB/month AWS free allowance; no extra wake-ups.

| Item | Basis | USD/month |
|---|---|---|
| EC2 `t3.medium` | 0.048/h × 261 h | 12.52 |
| EBS gp3 30 GB (root, exists only while the instance runs) | 0.0952/GB-mo × 30 × 261/730 | 1.02 |
| Public IPv4, host | 0.005/h × 261 h | 1.30 |
| RDS `db.t4g.micro` single-AZ | 0.019/h × 265 h | 5.03 |
| RDS gp3 20 GB (billed while stopped too) | 0.137/GB-mo × 20 | 2.74 |
| RDS backups (3 days, ≤ allocated storage) | free | 0.00 |
| ALB hours (always on) | 0.027/h × 730 h | 19.71 |
| ALB LCUs | 0.008/LCU-h, test traffic | 1.00 |
| Public IPv4, ALB (2 AZs, always on) | 0.005/h × 2 × 730 h | 7.30 |
| KMS `alias/dt-staging-data` | 1.00/key-mo (rotation adds up to 2.00 after the first years) | 1.00 |
| Secrets Manager (3 + RDS master secret) | 0.40/secret-mo, API calls negligible | 1.60 |
| Route 53 zone + queries | 0.50 + about 0.10 | 0.60 |
| CloudWatch: logs 2 GB, 4 alarms (5 alarm metrics), 1 agent metric | 0.63/GB ingest; 0.10/alarm metric; 0.30/metric | 2.06 |
| ECR storage (about 1.5 GB) | 0.10/GB-mo | 0.15 |
| S3 OSRM data (about 2 GB) and requests; S3 gateway endpoint | 0.0245/GB-mo; endpoint free | 0.10 |
| ACM certificate, EventBridge Scheduler, SNS email, NAT (none) | free | 0.00 |
| **Staging total** | | **56.13 (EUR 48-53)** |
| Bootstrap (KMS keys, S3, CloudTrail; aws-account-setup runbook) | | EUR 3-4 |
| **Everything, before VAT** | | **about EUR 51-57** |
| Everything including 21 % VAT | | about EUR 62-69 |

Against the budget of EUR 82 before VAT (EUR 100 with VAT) this leaves EUR 25-31 of headroom for extra wake-ups (about EUR 0.07/hour) and traffic.

Without the schedule (staging up 24/7) the EC2, EBS, IPv4 and RDS lines become USD 35.04 + 2.86 + 3.65 + 13.87: total about USD 92, EUR 79-87, with the bootstrap EUR 82-91, **at or over the budget**. The schedule is what makes staging fit.

### ALB or Caddy on the host

The plan allows replacing the ALB with Caddy on the host plus an Elastic IP (Let's Encrypt) if the estimate exceeds EUR 82. It does not (EUR 51-57), so **staging keeps the ALB**. For the record, the Caddy variant would save about USD 24/month (ALB hours, LCUs and its two IPv4 addresses, minus about USD 3.65 for an always-allocated Elastic IP) at the cost of: no ALB health-based routing or circuit-breaker signal from target health (rollback only on tasks that fail to start), a single TLS endpoint on the host (the API is down with the host, and certificate issuance depends on the host being up at renewal time), TLS keys on the host instead of ACM, and the WebSocket/SSE idle handling moved into Caddy's configuration.

## Known limits

- One host: a deploy or a host replacement is a short outage. Upgrade path: `instance_type = "t3.large"` (about EUR 30-35/month more running all hours, about EUR 11 with the schedule); tell the owner before.
- All application containers share the roles `dt-staging-task-execution` and `dt-staging-task` (the names the deploy role may pass). The task role can only read the OSRM bucket; the execution role can read all staging secrets, so any task definition the deploy role registers could reference any staging secret.
- The services keep their `mig_<svc>` URL at runtime (startup migrations, needed for deploys while asleep).
- The ports of NATS (4222) and Redis (6379, no password) listen on the host's interfaces; only the security group (ALB on 8080/8082 only) keeps them private. Never open the host security group further.
- No WAF: the region-lock SCP does not exempt `wafv2` for CloudFront, and a regional WAF costs more than the budget allows.
