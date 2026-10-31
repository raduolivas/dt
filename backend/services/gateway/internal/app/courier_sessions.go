package app

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"time"

	"github.com/raduolivas/dt/backend/pkg/courierjwt"
	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// Courier session errors. The token endpoint maps them to its public codes
// (link_code_invalid, link_code_used, refresh_token_invalid, session_ended).
var (
	// ErrCourierSessionNotFound is a session that does not exist or belongs
	// to another tenant or environment.
	ErrCourierSessionNotFound = errors.New("gateway: courier session not found")
	// ErrSessionMode is a mode other than sdk or link.
	ErrSessionMode = errors.New("gateway: courier session mode must be sdk or link")
	// ErrLinkCodeInvalid is a link code that is unknown, malformed, expired
	// or cancelled by a newer link.
	ErrLinkCodeInvalid = errors.New("gateway: link code invalid")
	// ErrLinkCodeUsed is a link code that was already exchanged.
	ErrLinkCodeUsed = errors.New("gateway: link code used")
	// ErrRefreshInvalid is a refresh token that is unknown or malformed.
	ErrRefreshInvalid = errors.New("gateway: refresh token invalid")
	// ErrRefreshReuse is a superseded refresh token: it was copied, so the
	// session has been ended (refresh_reuse).
	ErrRefreshReuse = errors.New("gateway: refresh token reused")
	// ErrSessionEnded is a refresh of a session that has ended or expired.
	ErrSessionEnded = errors.New("gateway: courier session ended")
)

// CourierTouchInterval is how stale last_seen_at may get before Touch
// writes it again.
const CourierTouchInterval = 60 * time.Second

// Audit actors of the session changes this service records itself (M5
// rulings R3, R5): a partner's start and end are audited by the HTTP Audit
// hook, but a link-code exchange, a courier's own end and the system's ends
// pass no such hook.
const (
	auditActorSystem = "system"
	auditSystemID    = "gateway"
	// auditNoStatus is AuditEntry.Status for these entries: 0 means no HTTP
	// response, the entry was written by the app layer.
	auditNoStatus = 0
)

// Tokens is a courier's token pair. The refresh token is shown once; only its
// keyed hash is stored.
type Tokens struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	SessionExpiresAt time.Time
}

// StartResult is a new session with, in sdk mode, its tokens, and in link
// mode, its link code (shown once) and the code's expiry.
type StartResult struct {
	Session       domain.CourierSession
	Tokens        Tokens
	LinkCode      string
	LinkExpiresAt time.Time
}

// CourierSessions starts, exchanges, refreshes, authenticates and ends
// courier sessions (M5 spec §6.1). A courier has at most one usable session:
// a session that becomes usable ends the one before it (replaced), and a new
// link cancels an older pending one.
type CourierSessions struct {
	Repo     CourierSessionStore
	Hasher   CourierTokenHasher
	Signer   *courierjwt.Signer
	Deny     CourierDenyList
	Couriers CourierLookup
	// Audit records the ends no HTTP Audit hook sees: the courier's own
	// and the system's (replaced, refresh_reuse).
	Audit AuditStore
	// RequestID returns the request id in ctx for those audit entries;
	// nil means none.
	RequestID func(context.Context) string
	Clock     Clock
	// Log defaults to a discarding logger.
	Log *slog.Logger
}

// Start opens a session for courierID in p's tenant and environment.
// expiresIn zero means the default 12 h. In sdk mode the session is usable at
// once, ends the courier's active session (replaced) and comes with tokens;
// in link mode it waits for its code to be exchanged and cancels the
// courier's older pending link. ErrCourierNotFound for an unknown courier.
func (c *CourierSessions) Start(ctx context.Context, p domain.Principal, courierID, mode string, expiresIn time.Duration) (StartResult, error) {
	m := domain.SessionMode(mode)
	if !m.Valid() {
		return StartResult{}, ErrSessionMode
	}
	length, err := domain.SessionLength(expiresIn)
	if err != nil {
		return StartResult{}, err
	}
	if err := c.Couriers.CourierExists(ctx, p, courierID); err != nil {
		if errors.Is(err, ErrCourierNotFound) {
			return StartResult{}, ErrCourierNotFound
		}
		return StartResult{}, fmt.Errorf("look up courier: %w", err)
	}
	now := c.Clock.Now()
	s := domain.CourierSession{ID: ids.New(ids.CourierSession), TenantID: p.TenantID, Env: p.Env, CourierID: courierID,
		Mode: m, State: domain.SessionPendingLink, CreatedAt: now, ExpiresIn: length}
	var res StartResult
	replace := domain.SessionPendingLink
	if m == domain.ModeSDK {
		refresh, err := domain.NewCourierRefreshToken()
		if err != nil {
			return StartResult{}, err
		}
		s.Activate(now)
		s.RefreshHash = c.Hasher.HashCourierRefresh(refresh)
		if res.Tokens, err = c.issue(s, refresh, now); err != nil {
			return StartResult{}, err
		}
		replace = domain.SessionActive
	} else {
		code, err := domain.NewCourierLinkCode()
		if err != nil {
			return StartResult{}, err
		}
		exp := now.Add(domain.LinkCodeTTL)
		s.LinkCodeHash, s.LinkExpiresAt = c.Hasher.HashCourierLink(code), &exp
		res.LinkCode, res.LinkExpiresAt = code, exp
	}
	var replaced *domain.CourierSession
	err = c.Repo.Update(ctx, s.TenantID, s.Env, s.CourierID, func(tx CourierSessionTx) error {
		var err error
		if replaced, err = endCurrent(tx, replace, now); err != nil {
			return err
		}
		if err := tx.Insert(s); err != nil {
			return err
		}
		if s.State == domain.SessionActive {
			return tx.Started(s)
		}
		return nil
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("start courier session: %w", err)
	}
	c.systemEnded(ctx, replaced, netip.Addr{})
	res.Session = s
	return res, nil
}

// Exchange trades a link code, once, for the session's first token pair. The
// session becomes usable and ends the courier's active session (replaced).
// ip is the courier's address, for the audit of that end.
func (c *CourierSessions) Exchange(ctx context.Context, code, ip string) (Tokens, error) {
	if !domain.ValidCourierLinkCode(code) {
		return Tokens{}, ErrLinkCodeInvalid
	}
	found, err := c.Repo.FindByLinkHash(ctx, c.Hasher.HashCourierLink(code))
	if errors.Is(err, ErrNotFound) {
		return Tokens{}, ErrLinkCodeInvalid
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("find link code: %w", err)
	}
	refresh, err := domain.NewCourierRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	now := c.Clock.Now()
	var tokens Tokens
	var replaced *domain.CourierSession
	err = c.Repo.Update(ctx, found.TenantID, found.Env, found.CourierID, func(tx CourierSessionTx) error {
		s, err := tx.Lock(found.ID)
		if err != nil {
			return err
		}
		if s.StartedAt != nil {
			return ErrLinkCodeUsed
		}
		if s.Status(now) != domain.SessionPendingLink { // expired or cancelled
			return ErrLinkCodeInvalid
		}
		if replaced, err = endCurrent(tx, domain.SessionActive, now); err != nil {
			return err
		}
		s.Activate(now)
		s.RefreshHash = c.Hasher.HashCourierRefresh(refresh)
		if tokens, err = c.issue(s, refresh, now); err != nil {
			return err
		}
		if err := tx.Save(s); err != nil {
			return err
		}
		return tx.Started(s)
	})
	if errors.Is(err, ErrLinkCodeUsed) || errors.Is(err, ErrLinkCodeInvalid) {
		return Tokens{}, err
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("exchange link code: %w", err)
	}
	addr, _ := netip.ParseAddr(ip)
	c.systemEnded(ctx, replaced, addr)
	c.record(ctx, found, string(domain.CredentialCourier), found.ID, "courier_sessions.start", addr)
	return tokens, nil
}

// Refresh rotates a refresh token: it returns a new pair, and the presented
// token stops working. A superseded token means a copy is in use: the session
// ends (refresh_reuse) and the answer is ErrRefreshReuse.
func (c *CourierSessions) Refresh(ctx context.Context, refresh string) (Tokens, error) {
	if !domain.ValidCourierRefreshToken(refresh) {
		return Tokens{}, ErrRefreshInvalid
	}
	h := c.Hasher.HashCourierRefresh(refresh)
	found, err := c.Repo.FindByRefreshHash(ctx, h)
	if errors.Is(err, ErrNotFound) {
		return Tokens{}, ErrRefreshInvalid
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("find refresh token: %w", err)
	}
	next, err := domain.NewCourierRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	now := c.Clock.Now()
	var tokens Tokens
	var reused *domain.CourierSession
	err = c.Repo.Update(ctx, found.TenantID, found.Env, found.CourierID, func(tx CourierSessionTx) error {
		s, err := tx.Lock(found.ID)
		if err != nil {
			return err
		}
		if s.Status(now) == domain.SessionEnded {
			return ErrSessionEnded
		}
		// Under the lock, a token that is not the current one has been
		// superseded (found it in the superseded set, or lost a race to a
		// concurrent refresh with the same token): reuse.
		if !hmac.Equal(s.RefreshHash, h) {
			s.End(domain.EndRefreshReuse, now)
			if err := tx.Save(s); err != nil {
				return err
			}
			reused = &s
			return tx.Ended(s)
		}
		s.RefreshHash = c.Hasher.HashCourierRefresh(next)
		s.RefreshGeneration++
		if tokens, err = c.issue(s, next, now); err != nil {
			return err
		}
		return tx.Save(s)
	})
	if errors.Is(err, ErrSessionEnded) {
		return Tokens{}, err
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("refresh courier session: %w", err)
	}
	if reused != nil {
		c.systemEnded(ctx, reused, netip.Addr{})
		return Tokens{}, ErrRefreshReuse
	}
	return tokens, nil
}

// RefreshSession returns the id of the session refresh belongs to, current
// or superseded, without changing anything: the token endpoint charges that
// session's rate bucket before it rotates the token. ErrRefreshInvalid for
// an unknown or malformed token.
func (c *CourierSessions) RefreshSession(ctx context.Context, refresh string) (string, error) {
	if !domain.ValidCourierRefreshToken(refresh) {
		return "", ErrRefreshInvalid
	}
	found, err := c.Repo.FindByRefreshHash(ctx, c.Hasher.HashCourierRefresh(refresh))
	if errors.Is(err, ErrNotFound) {
		return "", ErrRefreshInvalid
	}
	if err != nil {
		return "", fmt.Errorf("find refresh token: %w", err)
	}
	return found.ID, nil
}

// Authenticate verifies a courier access token and checks its session
// against the deny-list. Every bad or revoked token is ErrUnauthenticated; a
// deny-list that cannot be read is an error (fail closed).
func (c *CourierSessions) Authenticate(ctx context.Context, accessToken string) (domain.Principal, courierjwt.Claims, error) {
	claims, err := c.Signer.Verify(accessToken, c.Clock.Now())
	if err != nil {
		return domain.Principal{}, courierjwt.Claims{}, ErrUnauthenticated
	}
	env := domain.Environment(claims.Env)
	if !env.Valid() {
		return domain.Principal{}, courierjwt.Claims{}, ErrUnauthenticated
	}
	denied, err := c.Deny.Denied(ctx, claims.SessionID)
	if err != nil {
		return domain.Principal{}, courierjwt.Claims{}, fmt.Errorf("gateway: courier deny-list: %w", err)
	}
	if denied {
		return domain.Principal{}, courierjwt.Claims{}, ErrUnauthenticated
	}
	return domain.Principal{
		TenantID:     claims.TenantID,
		CredentialID: claims.SessionID,
		Env:          env,
		Credential:   domain.CredentialCourier,
		Scopes:       slices.Clone(domain.CourierScopes),
	}, claims, nil
}

// Get returns session id of p's tenant and environment as seen now (a lapsed
// session reads as ended, expired), or ErrCourierSessionNotFound.
func (c *CourierSessions) Get(ctx context.Context, p domain.Principal, id string) (domain.CourierSession, error) {
	s, err := c.scoped(ctx, p, id)
	if err != nil {
		return domain.CourierSession{}, err
	}
	return s.View(c.Clock.Now()), nil
}

// End ends session id of p's tenant and environment for reason (a partner
// passes "partner"), or returns ErrCourierSessionNotFound. Ending an ended
// session changes nothing and returns it.
func (c *CourierSessions) End(ctx context.Context, p domain.Principal, id, reason string) (domain.CourierSession, error) {
	s, err := c.scoped(ctx, p, id)
	if err != nil {
		return domain.CourierSession{}, err
	}
	return c.end(ctx, s, reason)
}

// EndSession ends session sid for reason on the courier's or the system's
// behalf (a courier passes "courier"); like End, it is idempotent.
func (c *CourierSessions) EndSession(ctx context.Context, sid, reason string) (domain.CourierSession, error) {
	s, err := c.Repo.Get(ctx, sid)
	if errors.Is(err, ErrNotFound) {
		return domain.CourierSession{}, ErrCourierSessionNotFound
	}
	if err != nil {
		return domain.CourierSession{}, fmt.Errorf("get courier session: %w", err)
	}
	return c.end(ctx, s, reason)
}

// Touch records that session sid was seen at now, at most once per
// CourierTouchInterval.
func (c *CourierSessions) Touch(ctx context.Context, sid string, now time.Time) error {
	if err := c.Repo.Touch(ctx, sid, now); err != nil {
		return fmt.Errorf("touch courier session: %w", err)
	}
	return nil
}

func (c *CourierSessions) scoped(ctx context.Context, p domain.Principal, id string) (domain.CourierSession, error) {
	s, err := c.Repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && (s.TenantID != p.TenantID || s.Env != p.Env)) {
		return domain.CourierSession{}, ErrCourierSessionNotFound
	}
	if err != nil {
		return domain.CourierSession{}, fmt.Errorf("get courier session: %w", err)
	}
	return s, nil
}

// end ends found for reason and deny-lists it. The deny-list is written on
// every call, also when the session had already ended, so a retry after a
// failed write still cuts the tokens off; a failed write is an error.
func (c *CourierSessions) end(ctx context.Context, found domain.CourierSession, reason string) (domain.CourierSession, error) {
	r := domain.EndReason(reason)
	if !validEndReason(r) {
		return domain.CourierSession{}, fmt.Errorf("gateway: unknown end reason %q", reason)
	}
	now := c.Clock.Now()
	var s domain.CourierSession
	var changed bool
	err := c.Repo.Update(ctx, found.TenantID, found.Env, found.CourierID, func(tx CourierSessionTx) error {
		var err error
		if s, err = tx.Lock(found.ID); err != nil {
			return err
		}
		if changed = s.End(r, now); !changed {
			return nil
		}
		if err := tx.Save(s); err != nil {
			return err
		}
		if s.StartedAt != nil {
			return tx.Ended(s)
		}
		return nil
	})
	if err != nil {
		return domain.CourierSession{}, fmt.Errorf("end courier session: %w", err)
	}
	if err := c.deny(ctx, s.ID); err != nil {
		return domain.CourierSession{}, err
	}
	if changed {
		c.audit(ctx, s, netip.Addr{})
	}
	return s.View(now), nil
}

// systemEnded finishes a session the system ended inside another change
// (replaced, refresh_reuse): it deny-lists and audits it. That change has
// committed, so a failed deny-list write is logged, not returned: the
// session's refresh token is already dead, and its access token lapses
// within courierjwt.TTL.
func (c *CourierSessions) systemEnded(ctx context.Context, s *domain.CourierSession, ip netip.Addr) {
	if s == nil {
		return
	}
	if err := c.deny(ctx, s.ID); err != nil {
		c.log().ErrorContext(ctx, "courier_session_deny_failed", "session_id", s.ID, "reason", string(s.EndReason),
			"error", err.Error())
	}
	c.audit(ctx, *s, ip)
}

func (c *CourierSessions) deny(ctx context.Context, sid string) error {
	var err error
	for range revokeAttempts {
		if err = c.Deny.Deny(ctx, sid); err == nil {
			return nil
		}
	}
	return fmt.Errorf("deny-list courier session %s: %w", sid, err)
}

// audit records the end of s when no HTTP Audit hook does: the courier's own
// end and the system's.
func (c *CourierSessions) audit(ctx context.Context, s domain.CourierSession, ip netip.Addr) {
	switch s.EndReason {
	case domain.EndCourier:
		c.record(ctx, s, string(domain.CredentialCourier), s.ID, "courier_sessions.end", ip)
	case domain.EndReplaced, domain.EndRefreshReuse:
		c.record(ctx, s, auditActorSystem, auditSystemID, "courier_sessions."+string(s.EndReason), ip)
	default: // partner (audited by the HTTP hook) and expired (no actor)
	}
}

// record writes one audit entry about session s. A failed write is logged;
// the change it records stands.
func (c *CourierSessions) record(ctx context.Context, s domain.CourierSession, actorType, actorID, action string, ip netip.Addr) {
	if c.Audit == nil {
		return
	}
	e := AuditEntry{TenantID: s.TenantID, Env: s.Env, ActorType: actorType, ActorID: actorID, Action: action,
		Target: s.ID, Status: auditNoStatus, IP: ip, At: c.Clock.Now()}
	if c.RequestID != nil {
		e.RequestID = c.RequestID(ctx)
	}
	if err := c.Audit.Record(ctx, e); err != nil {
		c.log().ErrorContext(ctx, "audit_write_failed", "error", err.Error(), "action", e.Action,
			"tenant_id", e.TenantID, "session_id", s.ID)
	}
}

// issue signs s's access token at now and pairs it with refresh. The access
// token never outlives the session (ruling R4): its exp is the earlier of
// now + courierjwt.TTL and the session's expiry.
func (c *CourierSessions) issue(s domain.CourierSession, refresh string, now time.Time) (Tokens, error) {
	access, exp, err := c.Signer.SignUntil(courierjwt.Claims{CourierID: s.CourierID, TenantID: s.TenantID, Env: string(s.Env),
		SessionID: s.ID, Source: string(s.Mode)}, now, *s.ExpiresAt)
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, AccessExpiresAt: exp, RefreshToken: refresh, SessionExpiresAt: *s.ExpiresAt}, nil
}

func (c *CourierSessions) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log
}

// endCurrent ends the courier's session stored as st, if any, as replaced
// (or expired, if it had lapsed) and returns it; nil when there was none.
// A session that never started (a pending link) gets no ended event.
func endCurrent(tx CourierSessionTx, st domain.SessionStatus, now time.Time) (*domain.CourierSession, error) {
	old, err := tx.Current(st)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !old.End(domain.EndReplaced, now) {
		return nil, nil
	}
	if err := tx.Save(old); err != nil {
		return nil, err
	}
	if old.StartedAt != nil {
		if err := tx.Ended(old); err != nil {
			return nil, err
		}
	}
	return &old, nil
}

func validEndReason(r domain.EndReason) bool {
	switch r {
	case domain.EndCourier, domain.EndPartner, domain.EndExpired, domain.EndReplaced, domain.EndRefreshReuse:
		return true
	}
	return false
}
