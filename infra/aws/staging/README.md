# infra/aws/staging

OpenTofu stack for the lean AWS staging environment in `dt-staging` (`eu-central-1`). Design: `docs/superpowers/specs/2026-10-26-m3-tracking-design.md` section 7.3. Owner steps, cost estimate and operations: `docs/runbooks/staging.md`.

## What it creates

| File | Resources |
|---|---|
| `network.tf` | VPC `10.40.0.0/16` over 2 AZs: two public subnets (ALB, host), two private DB subnets with no route out, **no NAT gateway**, S3 gateway endpoint, locked default SG. Security groups: ALB 443/80 from the internet; host only from the ALB on 8080 (gateway) and 8082 (tracking), egress 443 and 5432 to RDS; RDS 5432 only from the host |
| `dns.tf` | Hosted zone `staging.deliveryontrack.io` (`prevent_destroy`), ACM certificate for `api.staging.deliveryontrack.io` with `track.staging.deliveryontrack.io` as a SAN (DNS-validated), alias records `api` and `track` to the ALB |
| `cdn.tf` | Web CDN: private bucket `dt-staging-cdn-<account>` (SSE-S3, versioning off, all public access blocked, ACLs disabled, TLS-only policy, readable only by the distribution) with `page/`, `sdk/`, `tiles/`; CloudFront at `cdn.staging.deliveryontrack.io` (origin access control, HTTP/2 and HTTP/3, compression, price class 100, managed CachingOptimized, response headers policy with CORS `*` and `Cross-Origin-Resource-Policy: cross-origin`); its ACM certificate in `us-east-1` (provider `aws.us_east_1`); A/AAAA aliases; inline policy `cdn-upload` on the bootstrap's `dt-deploy-staging` (`s3:PutObject` on `page/*` and `sdk/*` only) |
| `kms.tf` | Key `alias/dt-staging-data` (rotation on, `prevent_destroy`) for Secrets Manager, RDS and the alarm topic |
| `secrets.tf` | `random_bytes` for the internal RPC token, `APIKEY_PEPPER`, the gateway's and deliveries' `DATA_KEYS`, `VIEWER_SIGNING_KEYS`, the `svc_*`/`mig_*` passwords; the internal mTLS CA and certificates (`tls` provider). Stored as three JSON secrets `dt-staging/app`, `dt-staging/db`, `dt-staging/mtls` |
| `rds.tf` | PostgreSQL 18 `db.t4g.micro`, 20 GB gp3, single-AZ, KMS-encrypted, `rds.force_ssl=1`, backups 3 days, RDS-managed master secret |
| `ecr.tf` | Repositories `dt-<svc>`: immutable tags, scan on push, keep the last 30 artifacts (image + signature of the last 15 deploys) |
| `ec2_ecs.tf` | Cluster `dt-staging`; ASG (min 0, max 1, `t3.medium`, ECS-optimized AL2023, 30 GB gp3 encrypted, IMDSv2 with hop limit 1, SSM, no key pair); capacity provider (managed scaling off); roles `dt-staging-task-execution`, `dt-staging-task`, `dt-staging-ecs-host`; log group `/dt/staging` (14 days) |
| `services.tf` | Task definitions (bridge, static host ports) and services for the 5 services, NATS, Redis and OSRM car/bike; `dt-staging-migrate-<svc>` and `dt-staging-db-bootstrap` one-off tasks; the OSRM data bucket |
| `alb.tf` | ALB (idle timeout 120 s), HTTPS listener (TLS 1.3/1.2 policy), HTTP → HTTPS redirect, `/v1/tracking/snapshot` and `/v1/tracking/stream*` → tracking, the rest → gateway (on both `api` and `track`) |
| `schedule.tf` | EventBridge Scheduler: database starts 07:50 and host scales to 1 at 08:00, both stop at 20:00, Monday–Friday, `Europe/Amsterdam` |
| `alarms.tf` | SNS email topic (KMS); host CPU > 80 % for 10 min, host memory > 90 % (CloudWatch agent), ALB target 5xx > 5 per 5 min, RDS free storage < 2 GB |

`files/user_data.sh.tftpl` configures the ECS agent, makes sure the SSM agent is installed and running, and starts the CloudWatch agent (memory only). `files/db-bootstrap.sh` runs `infra/compose/postgres/init/01-schemas.sql` on a fresh database and then sets the role passwords from Secrets Manager.

## How the containers find each other

One host, bridge networking, static host ports. Every container maps the names `gateway`, `deliveries`, `tracking`, `webhooks`, `accounts`, `nats`, `redis`, `osrm-car`, `osrm-bike` to the host's `docker0` address (`172.17.0.1`, variable `docker_bridge_ip`), so the URLs are the Compose stack's (`https://deliveries:8081`, `nats://nats:4222`, ...) and the mTLS certificates carry those names. Ports: gateway 8080, deliveries 8081, tracking 8082 (public) and 8085 (mTLS RPC), webhooks 8083, accounts 8084, NATS 4222, Redis 6379, OSRM car 5000, bike 5001. Only 8080 and 8082 are reachable from outside the host, and only from the ALB.

The services read the mTLS certificate, key and CA from files. A `certs` init container (busybox, user 65532) writes them from Secrets Manager into a task-scoped tmpfs volume that the service mounts read-only at `/certs`.

## Contract with the bootstrap

The names are fixed by `infra/aws/bootstrap/oidc.tf` (the deploy role's permissions): role `dt-deploy-staging` (this stack attaches the inline policy `cdn-upload` to it, so bootstrap phase B must have run), cluster `dt-staging`, repositories `dt-*` (the brief's `dt/<svc>` would not match the deploy role's `dt-*`), task definition families `dt-staging-*`, roles `dt-staging-task-execution` and `dt-staging-task`. The deploy role has no `ecs:TagResource`, so services set `enable_ecs_managed_tags = false` and `propagate_tags = "NONE"`, and the deploy never passes tags.

## Running it

Only through `make staging-up` / `make staging-down` (`scripts/staging.sh`), from a **management-account Identity Center Admin session**: the backend is the bootstrap's state bucket (key `staging/terraform.tfstate`, SSE-KMS, `use_lockfile`), which has no cross-account grants; the provider then assumes `OrganizationAccountAccessRole` in `dt-staging`. Variables go in `terraform.tfvars` (git-ignored; copy `terraform.tfvars.example`).

`make staging-down` destroys every resource except the hosted zone and the KMS key and alias (`prevent_destroy`); the state bucket belongs to the bootstrap. A plain `tofu destroy` fails on purpose because of `prevent_destroy`.

Local checks without credentials:

```sh
cd infra/aws/staging
tofu init -backend=false && tofu validate && tofu fmt -check
trivy config --severity HIGH,CRITICAL infra/aws   # from the repository root
```

Pinned: OpenTofu 1.13.2, AWS provider `~> 6.70.0`, random `~> 3.7`, tls `~> 4.1` (`.terraform.lock.hcl` committed for linux amd64/arm64 and macOS arm64).

## Web CDN and the hosted page (M4)

- The gateway serves the page shell at `https://track.staging.deliveryontrack.io/t/<token>` and links `entry.js`/`entry.css` from `TRACKING_PAGE_BASE` = `https://cdn.staging.deliveryontrack.io/page/<sha>/`. Tofu writes `<sha>` = `image_tag` (`bootstrap`, like its image tag); each deploy rewrites it to its own commit in the revisions it registers, right after uploading `page/<sha>/` and `sdk/tracking/<sha>/` (`scripts/web-staging-build.sh`, never the local `make web-assets` output).
- The other page settings: `TRACKING_PAGE_CDN` = `https://cdn.staging.deliveryontrack.io`, `TRACKING_PAGE_TILES` = `https://cdn.staging.deliveryontrack.io/tiles/nl.pmtiles`, `TRACKING_PAGE_API` = `https://api.staging.deliveryontrack.io/v1`, `TRACKING_URL_BASE` = `https://track.staging.deliveryontrack.io`.
- Old `page/` and `sdk/` prefixes stay (open pages keep working); the deploy role cannot delete or list. Tiles are the owner's: `make tiles-nl`, then `make tiles-upload` (Admin), see `docs/runbooks/tiles.md`.
- `tests/cdn.tftest.hcl` (offline) guards the bucket's privacy, OAC, the `cdn.staging` alias, the deploy role's `page/*`/`sdk/*` limit, the `track` SAN and alias, and the gateway's five `TRACKING_*` values.

## Notes

- Task definitions in tofu start with the image tag `bootstrap`, which does not exist: the services cannot start until the first deploy pushes images and registers new revisions. The services ignore `task_definition` changes; after changing environment variables here, run the deploy workflow (it copies the latest revision of each family and swaps only the image).
- Only the `dt-staging-migrate-<svc>` tasks get `MIGRATIONS_DATABASE_URL` (the schema owner `mig_<svc>`) and run with `MIGRATIONS=only`; the services run with `MIGRATIONS=skip` and only their `svc_<svc>` URL, so a running service cannot alter its tables (for example turn row-level security off). A deploy while staging is asleep therefore only registers the new revisions and stops.
- Memory: each main container reserves the plan's table value (`memoryReservation`, total 3208 MB, 3336 MB with the init containers) and has a higher hard limit (`memory`); there is no task-level memory.
- `files/db-bootstrap.sh` runs in a single transaction and sets the real role passwords in it, so a failed first run leaves nothing behind.
- Every trivy ignore in this stack names its reason next to it.
