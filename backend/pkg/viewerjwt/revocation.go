package viewerjwt

// Link revocation in Redis, shared by the gateway (which writes it) and the
// tracking service (which enforces it on open streams).
const (
	// RevocationsChannel carries the id of every revoked tracking link.
	RevocationsChannel = "dt:viewer:revocations"
	// RevokedTTL is how long a revoked link id stays deny-listed: as long as
	// a token exchanged for it can still verify (its lifetime plus the exp
	// leeway).
	RevokedTTL = TTL + Leeway
)

// RevokedKey is the deny-list key of a revoked tracking link.
func RevokedKey(linkID string) string { return "dt:viewer:revoked:" + linkID }
