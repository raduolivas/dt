package domain

import "slices"

// CredentialType is the kind of credential a request authenticated with.
type CredentialType string

// CredentialSecretKey is a partner secret key (dt_sk_…). M3 adds courier and
// viewer tokens.
const CredentialSecretKey CredentialType = "secret_key"

// Principal is who an authenticated request acts as.
type Principal struct {
	TenantID, CredentialID string
	Env                    Environment
	Credential             CredentialType
	Scopes                 []Scope
}

// Has reports whether the principal carries scope s.
func (p Principal) Has(s Scope) bool { return slices.Contains(p.Scopes, s) }
