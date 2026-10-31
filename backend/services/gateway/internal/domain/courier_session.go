package domain

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"
)

// CredentialCourier is a courier access token (aud courier), issued to a
// courier session.
const CredentialCourier CredentialType = "courier"

// The scopes every courier token carries (M5 spec §6.1). They are not in
// AllScopes: a secret key never carries them.
const (
	ScopeLocationWrite Scope = "location:write"
	ScopeStatusWrite   Scope = "status:write"
)

// CourierScopes are the scopes of a courier principal.
var CourierScopes = []Scope{ScopeLocationWrite, ScopeStatusWrite}

// SessionMode is how a courier session starts: the partner's app holds the
// tokens (sdk), or the courier opens a link (link).
type SessionMode string

const (
	ModeSDK  SessionMode = "sdk"
	ModeLink SessionMode = "link"
)

// Valid reports whether m is a known mode.
func (m SessionMode) Valid() bool { return m == ModeSDK || m == ModeLink }

// SessionStatus is a courier session's lifecycle state.
type SessionStatus string

const (
	SessionPendingLink SessionStatus = "pending_link"
	SessionActive      SessionStatus = "active"
	SessionEnded       SessionStatus = "ended"
)

// EndReason says why a courier session ended.
type EndReason string

const (
	EndCourier      EndReason = "courier"
	EndPartner      EndReason = "partner"
	EndExpired      EndReason = "expired"
	EndReplaced     EndReason = "replaced"
	EndRefreshReuse EndReason = "refresh_reuse"
)

// Session lengths fixed by M5 Global Constraints.
const (
	// DefaultSessionLength applies when the partner asks for none.
	DefaultSessionLength = 12 * time.Hour
	// MinSessionLength and MaxSessionLength bound expires_in_seconds.
	MinSessionLength = 15 * time.Minute
	MaxSessionLength = 12 * time.Hour
	// LinkCodeTTL is how long a link code can be exchanged.
	LinkCodeTTL = 15 * time.Minute
)

// ErrSessionLength is a requested session length outside 900–43200 s.
var ErrSessionLength = errors.New("courier session: expires_in_seconds must be within 900-43200")

// SessionLength returns the session length for a requested one: zero means
// the default, anything else must be whole seconds within the bounds.
func SessionLength(requested time.Duration) (time.Duration, error) {
	if requested == 0 {
		return DefaultSessionLength, nil
	}
	if requested < MinSessionLength || requested > MaxSessionLength || requested%time.Second != 0 {
		return 0, ErrSessionLength
	}
	return requested, nil
}

// CourierSession is one courier's shift on one phone. Only keyed hashes of
// its link code and refresh token are ever kept; the secrets themselves are
// shown once.
type CourierSession struct {
	ID, TenantID, CourierID string
	Env                     Environment
	Mode                    SessionMode
	// State is the stored state. A session past its expiry is still stored
	// as active (expiry is lazy); Status and View give the state at a time.
	State     SessionStatus
	CreatedAt time.Time
	// StartedAt and ExpiresAt are set when the session becomes usable.
	StartedAt, ExpiresAt *time.Time
	EndedAt              *time.Time
	EndReason            EndReason // "" until ended
	// ExpiresIn is the session length, counted from activation.
	ExpiresIn time.Duration
	// LinkCodeHash and LinkExpiresAt are set in link mode only. The hash is
	// kept after the exchange, so a second exchange is told the code was used.
	LinkCodeHash  []byte
	LinkExpiresAt *time.Time
	// RefreshHash is the hash of the current refresh token (nil while
	// pending); RefreshGeneration counts rotations.
	RefreshHash       []byte
	RefreshGeneration int
	LastSeenAt        *time.Time
}

// Activate makes a pending session usable from now until now + ExpiresIn.
func (s *CourierSession) Activate(now time.Time) {
	exp := now.Add(s.ExpiresIn)
	s.State, s.StartedAt, s.ExpiresAt = SessionActive, &now, &exp
}

// lapsedAt returns when a session that is not stored as ended stopped being
// valid by time alone (session or link code expiry), if that is before or at
// now.
func (s CourierSession) lapsedAt(now time.Time) (time.Time, bool) {
	switch s.State {
	case SessionActive:
		if s.ExpiresAt != nil && !now.Before(*s.ExpiresAt) {
			return *s.ExpiresAt, true
		}
	case SessionPendingLink:
		if s.LinkExpiresAt != nil && !now.Before(*s.LinkExpiresAt) {
			return *s.LinkExpiresAt, true
		}
	}
	return time.Time{}, false
}

// End ends the session at now for reason and reports whether it changed
// anything. A session already ended keeps its first reason; one that had
// already lapsed is recorded as expired at its expiry.
func (s *CourierSession) End(reason EndReason, now time.Time) bool {
	if s.State == SessionEnded {
		return false
	}
	if at, ok := s.lapsedAt(now); ok {
		reason, now = EndExpired, at
	}
	s.State, s.EndedAt, s.EndReason = SessionEnded, &now, reason
	return true
}

// Status is the session's state at now: a lapsed session reads as ended.
func (s CourierSession) Status(now time.Time) SessionStatus {
	if _, ok := s.lapsedAt(now); ok {
		return SessionEnded
	}
	return s.State
}

// View is the session as seen at now: a lapsed session is shown ended with
// reason expired at its expiry. s itself is unchanged.
func (s CourierSession) View(now time.Time) CourierSession {
	if at, ok := s.lapsedAt(now); ok {
		s.State, s.EndedAt, s.EndReason = SessionEnded, &at, EndExpired
	}
	return s
}

// IsUsable reports whether the session's tokens are good at now: active and
// not past its expiry.
func (s CourierSession) IsUsable(now time.Time) bool { return s.Status(now) == SessionActive }

// courier secret sizes: a link code is 128 bits, a refresh token 256 bits.
const (
	linkCodeBytes     = 16
	refreshTokenBytes = 32
)

// NewCourierLinkCode returns a fresh 128-bit link code, base64url without
// padding (22 characters).
func NewCourierLinkCode() (string, error) { return randomSecret(linkCodeBytes) }

// NewCourierRefreshToken returns a fresh 256-bit refresh token, base64url
// without padding (43 characters).
func NewCourierRefreshToken() (string, error) { return randomSecret(refreshTokenBytes) }

// ValidCourierLinkCode reports whether s has the shape of a link code, so a
// malformed value never reaches the store.
func ValidCourierLinkCode(s string) bool { return validSecret(s, linkCodeBytes) }

// ValidCourierRefreshToken reports whether s has the shape of a refresh
// token.
func ValidCourierRefreshToken(s string) bool { return validSecret(s, refreshTokenBytes) }

func randomSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("courier secret: %w", err)
	}
	return linkEncoding.EncodeToString(b), nil
}

func validSecret(s string, n int) bool {
	if len(s) != linkEncoding.EncodedLen(n) {
		return false
	}
	b, err := linkEncoding.DecodeString(s)
	return err == nil && len(b) == n
}
