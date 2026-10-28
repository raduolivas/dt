# Every secret is generated here (random_bytes, tls) and stored in Secrets
# Manager under alias/dt-staging-data; ECS injects them as environment
# variables (execution role). Nothing is printed: no output carries a value,
# and the state is SSE-KMS in the management account's state bucket.
#
# Three JSON secrets (Secrets Manager bills per secret), plus the RDS-managed
# master secret. Containers get only the JSON keys they need.

resource "random_bytes" "internal_rpc_token" {
  length = 32 # hex: 64 characters (rpcauth.MinTokenLen is 32)
}

resource "random_bytes" "apikey_pepper" {
  length = 32
}

resource "random_bytes" "gateway_data_key" {
  length = 32 # sealbox: 32-byte AES-256 key
}

resource "random_bytes" "deliveries_data_key" {
  length = 32
}

resource "random_bytes" "viewer_signing_seed" {
  length = 32 # Ed25519 seed
}

locals {
  db_roles = flatten([for s in local.services : ["svc_${s}", "mig_${s}"]])
}

resource "random_bytes" "db_password" {
  for_each = toset(local.db_roles)

  length = 24 # hex: 48 URL-safe characters
}

# ---- internal mTLS (Task 1): one CA, one certificate per service and dtctl ---

locals {
  mtls_names = ["gateway", "deliveries", "tracking", "accounts", "dtctl"]
}

resource "tls_private_key" "ca" {
  algorithm   = "ECDSA"
  ecdsa_curve = "P256"
}

resource "tls_self_signed_cert" "ca" {
  private_key_pem       = tls_private_key.ca.private_key_pem
  is_ca_certificate     = true
  validity_period_hours = 24 * 365 * 3
  early_renewal_hours   = 24 * 60

  subject {
    common_name  = "DT staging internal CA"
    organization = "DT"
  }

  allowed_uses = ["cert_signing", "crl_signing"]
}

resource "tls_private_key" "svc" {
  for_each = toset(local.mtls_names)

  algorithm   = "ECDSA"
  ecdsa_curve = "P256"
}

resource "tls_cert_request" "svc" {
  for_each = toset(local.mtls_names)

  private_key_pem = tls_private_key.svc[each.key].private_key_pem
  # Clients dial https://<name>:<port>; dtctl reaches accounts through an SSM
  # port forward on localhost.
  dns_names    = [each.key, "localhost"]
  ip_addresses = ["127.0.0.1"]

  subject {
    common_name = each.key
  }
}

resource "tls_locally_signed_cert" "svc" {
  for_each = toset(local.mtls_names)

  cert_request_pem      = tls_cert_request.svc[each.key].cert_request_pem
  ca_private_key_pem    = tls_private_key.ca.private_key_pem
  ca_cert_pem           = tls_self_signed_cert.ca.cert_pem
  validity_period_hours = 24 * 825
  early_renewal_hours   = 24 * 60

  allowed_uses = ["digital_signature", "server_auth", "client_auth"]
}

# ---- Secrets Manager --------------------------------------------------------

locals {
  db_host = aws_db_instance.this.address

  db_url = { for r in local.db_roles :
    r => "postgres://${r}:${random_bytes.db_password[r].hex}@${local.db_host}:5432/dt?sslmode=require"
  }

  app_secret = {
    internal_rpc_token = random_bytes.internal_rpc_token.hex
    apikey_pepper      = random_bytes.apikey_pepper.base64
    # <id>:<base64 key>; the ids name the key generation (data-key-rotation runbook).
    gateway_data_keys    = "gw1:${random_bytes.gateway_data_key.base64}"
    deliveries_data_keys = "dk1:${random_bytes.deliveries_data_key.base64}"
    viewer_signing_keys  = "vw1:${random_bytes.viewer_signing_seed.base64}"
  }

  db_secret = merge(
    { for r in local.db_roles : "${r}_url" => local.db_url[r] },
    { for r in local.db_roles : "${r}_password" => random_bytes.db_password[r].hex },
  )

  mtls_secret = merge(
    { ca_pem = tls_self_signed_cert.ca.cert_pem },
    { for n in local.mtls_names : "${n}_pem" => tls_locally_signed_cert.svc[n].cert_pem },
    { for n in local.mtls_names : "${n}_key" => tls_private_key.svc[n].private_key_pem_pkcs8 },
  )
}

resource "aws_secretsmanager_secret" "app" {
  name                    = "dt-staging/app"
  description             = "Internal RPC token, API key pepper, data keys, viewer signing keys"
  kms_key_id              = aws_kms_key.data.arn
  recovery_window_in_days = 0 # staging-down then staging-up reuses the name at once
}

resource "aws_secretsmanager_secret_version" "app" {
  secret_id     = aws_secretsmanager_secret.app.id
  secret_string = jsonencode(local.app_secret)
}

resource "aws_secretsmanager_secret" "db" {
  name                    = "dt-staging/db"
  description             = "Database URLs and passwords of the svc_* and mig_* roles"
  kms_key_id              = aws_kms_key.data.arn
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "db" {
  secret_id     = aws_secretsmanager_secret.db.id
  secret_string = jsonencode(local.db_secret)
}

resource "aws_secretsmanager_secret" "mtls" {
  name                    = "dt-staging/mtls"
  description             = "Internal mTLS CA certificate, service and dtctl certificates and keys"
  kms_key_id              = aws_kms_key.data.arn
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "mtls" {
  secret_id     = aws_secretsmanager_secret.mtls.id
  secret_string = jsonencode(local.mtls_secret)
}
