package viewerjwt

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Link revocation and expiry in Redis, shared by the gateway (which writes
// them) and the tracking service (which enforces them on open streams).
const (
	// RevocationsChannel carries the id of every revoked tracking link.
	RevocationsChannel = "dt:viewer:revocations"
	// RevokedTTL is how long a revoked link id stays deny-listed: as long as
	// a token exchanged for it can still verify (its lifetime plus the exp
	// leeway).
	RevokedTTL = TTL + Leeway
	// ExpiriesChannel carries "<tl_id> <unix seconds>" when a tracking link
	// gets its expiry, so tracking closes the link's open streams then. One
	// channel for every link keeps tracking at a single subscription.
	ExpiriesChannel = "dt:viewer:expiries"
)

// RevokedKey is the deny-list key of a revoked tracking link.
func RevokedKey(linkID string) string { return "dt:viewer:revoked:" + linkID }

// ExpiryKey holds a tracking link's expiry as unix seconds, for streams
// that open after it was announced on ExpiriesChannel.
func ExpiryKey(linkID string) string { return "dt:viewer:expiry:" + linkID }

// ExpiryKeyTTL is how long ExpiryKey is kept for a link expiring at
// expiresAt, seen at now: until the last token exchanged before the expiry
// can no longer verify (RevokedTTL past it).
func ExpiryKeyTTL(expiresAt, now time.Time) time.Duration {
	if d := expiresAt.Sub(now); d > 0 {
		return d + RevokedTTL
	}
	return RevokedTTL
}

// EncodeExpiry is the ExpiriesChannel payload for a link.
func EncodeExpiry(linkID string, expiresAt time.Time) string {
	return linkID + " " + strconv.FormatInt(expiresAt.Unix(), 10)
}

// DecodeExpiry parses an ExpiriesChannel payload.
func DecodeExpiry(payload string) (linkID string, expiresAt time.Time, err error) {
	id, sec, ok := strings.Cut(payload, " ")
	if !ok || id == "" || strings.ContainsAny(id, " \t\r\n") {
		return "", time.Time{}, errors.New("viewerjwt: malformed expiry payload")
	}
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return "", time.Time{}, errors.New("viewerjwt: malformed expiry payload")
	}
	return id, time.Unix(n, 0).UTC(), nil
}
