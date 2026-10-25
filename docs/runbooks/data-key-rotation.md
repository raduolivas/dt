# Runbook: data key rotation

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
