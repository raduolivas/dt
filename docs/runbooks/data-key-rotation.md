# Runbook: key rotation (data keys and viewer signing keys)

`deliveries` and the gateway seal personal data with AES-256-GCM (`pkg/sealbox`). Each service has its own
key ring, set by two environment variables:

| Variable | Meaning |
|---|---|
| `DATA_KEYS` | `id:base64[,id:base64...]`: every key the service can **open** with. Ids match `[a-z0-9]{1,16}`, each key decodes to 32 bytes. |
| `DATA_KEY_CURRENT` | The id that **seals** new values. It must be one of the ids in `DATA_KEYS`, or the service refuses to start. |

Every sealed value starts with the id of the key that sealed it, so old values stay readable as long as
their key is still in `DATA_KEYS`.

What each ring protects:

- **deliveries:** recipient name, notes, dropoff label, metadata and history reasons. These live as long as
  the delivery (up to the 13-month retention job).
- **gateway:** stored idempotency responses. These live 24 h.

Until a cloud KMS wraps the keys (first staging deploy, AWS KMS per D23), the keys are plain environment
secrets. Generate one with `openssl rand -base64 32`. Never commit one; the compose values are dev-only
placeholders.

## Rotate

Do it one service at a time. The example rotates `k1` to `k2`.

1. **Add the new key without using it.** Set `DATA_KEYS=k1:<old>,k2:<new>` and keep `DATA_KEY_CURRENT=k1`.
   Roll it out to **every replica** and wait until all of them are ready (`/readyz` 200).
   If you skip this step, a replica still on the old ring cannot open what a new replica seals with `k2`.
2. **Switch sealing to the new key.** Set `DATA_KEY_CURRENT=k2` (ring unchanged) and roll out again. From now
   on new values are sealed with `k2`; values sealed with `k1` still open.
3. **Retire the old key, only when nothing needs it any more:**
   - **gateway:** keep `k1` in the ring for **at least 24 h** after step 2 finished on the last replica
     (idempotency records expire after 24 h). Then set `DATA_KEYS=k2:<new>` and roll out.
   - **deliveries:** keep `k1` **until every value sealed with it has been re-sealed**. The re-seal job
     comes with the KMS work at the first staging deploy; **until then deliveries never retires a key**.

## A missing key

If a sealed value names a key that is not in the ring, opening it fails:

- the request returns `500`;
- the service logs `sealbox: cannot open sealed value`. In the gateway it is the `idempotency_unavailable`
  event with `error: open stored body: sealbox: cannot open sealed value`, on a retried request whose
  stored response was sealed with the missing key.

The same error also covers tampering and a wrong binding, so first check which key ids the replica
has (from its deployment config, never by printing the values).

Typical causes: a replica was left on an old ring (step 1 not rolled out everywhere), or a key was retired
too early (step 3).

## Roll back

- **During step 1 or 2:** put the previous `DATA_KEYS` / `DATA_KEY_CURRENT` back. If any replica already
  sealed with `k2`, keep `k2` in `DATA_KEYS` (only switch `DATA_KEY_CURRENT` back to `k1`), or those values
  become unreadable.
- **After step 3:** add the retired key back to `DATA_KEYS` and roll out. Values sealed with it open again.
  This is why a retired key is archived in the secrets store, not deleted, until its data is gone.

## Viewer signing keys (`VIEWER_SIGNING_KEYS`)

The gateway signs viewer JWTs (Ed25519, EdDSA) for tracking links and publishes the public keys at
`/.well-known/jwks.json`; tracking verifies against that JWKS. Two variables, the same shape as the data keys:

| Variable | Meaning |
|---|---|
| `VIEWER_SIGNING_KEYS` | `kid:base64(32-byte seed)[,kid:base64...]`. Every key listed is **published in the JWKS** (the public part), whether or not it signs. The `kid` is the id before the colon. |
| `VIEWER_SIGNING_KEY_CURRENT` | The `kid` that **signs** new tokens. It must be in `VIEWER_SIGNING_KEYS`, or the gateway refuses to start. |

Only the gateway holds these variables. A viewer token lives **15 minutes** (`viewerjwt.TTL`) plus **30 s** of
clock leeway, and verifiers cache the JWKS, so a retired key must stay published long enough for every token it
signed to expire. Rotating never signs anyone out: a viewer refreshes through `/tracking/session/refresh`,
which signs with the current key.

Rotate `vw1` to `vw2`:

1. **Publish the new key without signing with it.** Set `VIEWER_SIGNING_KEYS=vw1:<old>,vw2:<new>` and keep
   `VIEWER_SIGNING_KEY_CURRENT=vw1`. Roll out to **every gateway replica** and wait for `/readyz` 200.
   Tracking fetches the JWKS lazily (at least 1 s apart), so wait a few minutes before step 2 so it knows `vw2`.
   If you skip this, tracking rejects tokens signed with a `kid` it has not seen yet.
2. **Switch signing.** Set `VIEWER_SIGNING_KEY_CURRENT=vw2` (ring unchanged) and roll out.
3. **Retire the old key** after **at least 15 minutes + 30 s leeway (use 20 minutes, or 1 h for a margin)** from the last replica finishing step 2, plus the JWKS cache time of tracking. Then set
   `VIEWER_SIGNING_KEYS=vw2:<new>` and roll out. Tokens still signed by `vw1` are rejected after that, and the
   viewer silently refreshes with the session token.
4. **Roll back** by putting the previous values back; keep `vw2` in the ring if any replica already signed with it.

Compromised signing key: skip the wait. Switch `CURRENT`, drop the old key at once and roll out; every viewer
re-establishes a session within one refresh. To cut off one leaked link instead, rotate or revoke the link
(revocation is immediate and does not depend on keys).

## Staging (AWS Secrets Manager)

On staging the values are generated by OpenTofu (`infra/aws/staging/secrets.tf`) and stored in Secrets Manager
under the customer-managed KMS key `alias/dt-staging-data`; ECS injects them into the tasks at start. They are
never printed.

| Secret | Keys relevant here |
|---|---|
| `dt-staging/app` | `viewer_signing_keys` (`vw1:<base64>`), the gateway's and deliveries' data keys, `internal_rpc_token`, `apikey_pepper` |
| `dt-staging/db` | the `svc_*` / `mig_*` passwords |
| `dt-staging/mtls` | the internal CA and each service certificate |

The `*_CURRENT` ids are fixed in `infra/aws/staging/services.tf` (`DATA_KEY_CURRENT`,
`VIEWER_SIGNING_KEY_CURRENT = "vw1"`), not in the secret. To rotate on staging, from a management-account Admin
session (see `docs/runbooks/staging.md`, "Secrets"):

1. In `secrets.tf` add a second `random_bytes` (for example `viewer_signing_seed_2`) and extend the secret's
   value to `vw1:<old>,vw2:<new>` (data keys likewise: `gw2:` / `dk2:` next to the old entry). Leave `*_CURRENT`
   in `services.tf` unchanged. Run `make staging-up`, then re-run the deploy (a task reads the secret only when it starts).
2. Change `*_CURRENT` in `services.tf` to the new id, `make staging-up`, deploy again.
3. After the waiting time above (viewer keys 20 min or more; gateway data keys 24 h; deliveries data keys
   never until the re-seal job exists), remove the old entry from `secrets.tf`, apply and deploy again.

Never use `tofu apply -replace` on a key that has already sealed or signed anything you still need: the old
key disappears at once. It is fine for `internal_rpc_token` and the database passwords. `apikey_pepper` is not (it invalidates every API key). Staging data is simulated, so a mistake costs a `make staging-down` / `make staging-up`, not customer data.
