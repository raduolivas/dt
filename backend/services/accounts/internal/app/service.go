package app

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
)

// Event types published to the outbox.
const (
	EventKeyCreated   = "apikey.created"
	EventKeyExpirySet = "apikey.expiry_set"
	EventKeyRevoked   = "apikey.revoked"
	// EventTenantSettingsSet is tenant-level: its envelope has no environment.
	EventTenantSettingsSet = "tenant.settings_set"
)

const maxNameLen = 100

// Service holds the accounts use cases.
type Service struct {
	Repo  Repo
	Keys  KeyGenerator
	Clock Clock
}

// CreateKeyInput is the input of CreateKey.
type CreateKeyInput struct {
	Actor          domain.Actor
	TenantID, Name string
	Env            domain.Environment
	Scopes         []string
	AllowedIPs     []string
}

// CreateTenant creates a tenant (system use; no actor).
func (s *Service) CreateTenant(ctx context.Context, name string) (domain.Tenant, error) {
	name, err := validName(name)
	if err != nil {
		return domain.Tenant{}, err
	}
	now := s.Clock.Now()
	settings := domain.DefaultTenantSettings()
	settings.UpdatedAt = now
	t := domain.Tenant{ID: ids.New(ids.Tenant), Name: name, CreatedAt: now, Settings: settings}
	err = s.Repo.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.InsertTenant(ctx, t); err != nil {
			return err
		}
		return publishSettings(ctx, tx, t.ID, settings)
	})
	if err != nil {
		return domain.Tenant{}, err
	}
	return t, nil
}

// SetTenantSettings replaces a tenant's delivery settings and announces them.
// The caller's UpdatedAt is ignored; the clock stamps it.
func (s *Service) SetTenantSettings(ctx context.Context, actor domain.Actor, tenantID string, in domain.TenantSettings) (domain.TenantSettings, error) {
	if err := domain.CanSetSettings(actor); err != nil {
		return domain.TenantSettings{}, err
	}
	in.AllowedModels = slices.Clone(in.AllowedModels)
	in.UpdatedAt = s.Clock.Now()
	if err := in.Validate(); err != nil {
		return domain.TenantSettings{}, err
	}
	err := s.Repo.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.SetTenantSettings(ctx, tenantID, in); err != nil {
			return err
		}
		return publishSettings(ctx, tx, tenantID, in)
	})
	if err != nil {
		return domain.TenantSettings{}, err
	}
	return in, nil
}

func publishSettings(ctx context.Context, tx Tx, tenantID string, st domain.TenantSettings) error {
	return tx.Publish(ctx, EventTenantSettingsSet, tenantID, "", &eventsv1.TenantSettingsSet{
		TenantId: tenantID, AllowedModels: st.AllowedModels, DefaultModel: st.DefaultModel,
		PickupConfirmation: st.PickupConfirmation, UpdatedAt: timestamppb.New(st.UpdatedAt),
	})
}

// SetLiveEnabled flips the live-mode flag (system use; no actor).
func (s *Service) SetLiveEnabled(ctx context.Context, tenantID string, v bool) error {
	return s.Repo.InTx(ctx, func(ctx context.Context, tx Tx) error {
		return tx.SetLiveEnabled(ctx, tenantID, v)
	})
}

// CreateKey mints a key and returns it with its one-time plaintext.
func (s *Service) CreateKey(ctx context.Context, in CreateKeyInput) (domain.APIKey, string, error) {
	if !in.Env.Valid() {
		return domain.APIKey{}, "", fmt.Errorf("%w: environment", domain.ErrInvalidArgument)
	}
	if err := domain.CanManage(in.Actor, in.Env); err != nil {
		return domain.APIKey{}, "", err
	}
	name, err := validName(in.Name)
	if err != nil {
		return domain.APIKey{}, "", err
	}
	scopes, err := domain.ValidateScopes(in.Scopes)
	if err != nil {
		return domain.APIKey{}, "", err
	}
	ips, err := parseAllowedIPs(in.AllowedIPs)
	if err != nil {
		return domain.APIKey{}, "", err
	}

	var key domain.APIKey
	var plain string
	err = s.Repo.InTx(ctx, func(ctx context.Context, tx Tx) error {
		t, err := tx.GetTenant(ctx, in.TenantID)
		if err != nil {
			return err
		}
		if in.Env == domain.EnvLive && !t.LiveEnabled {
			return domain.ErrLiveNotEnabled
		}
		key, plain, err = s.insertKey(ctx, tx, in.TenantID, name, in.Env, scopes, ips, in.Actor)
		return err
	})
	if err != nil {
		return domain.APIKey{}, "", err
	}
	return key, plain, nil
}

// RollKey creates a replacement key with the same settings and starts the
// 24h overlap on the old one.
func (s *Service) RollKey(ctx context.Context, actor domain.Actor, tenantID, keyID string) (domain.APIKey, string, domain.APIKey, error) {
	var newKey, old domain.APIKey
	var plain string
	err := s.Repo.InTx(ctx, func(ctx context.Context, tx Tx) error {
		k, err := tx.GetKeyForUpdate(ctx, tenantID, keyID)
		if err != nil {
			return err
		}
		if err := domain.CanManage(actor, k.Env); err != nil {
			return err
		}
		exp, err := k.Roll(s.Clock.Now())
		if err != nil {
			return err
		}
		if k.Env == domain.EnvLive {
			t, err := tx.GetTenant(ctx, tenantID)
			if err != nil {
				return err
			}
			if !t.LiveEnabled {
				return domain.ErrLiveNotEnabled
			}
		}
		newKey, plain, err = s.insertKey(ctx, tx, tenantID, k.Name, k.Env, slices.Clone(k.Scopes), slices.Clone(k.AllowedIPs), actor)
		if err != nil {
			return err
		}
		if err := tx.UpdateKey(ctx, k); err != nil {
			return err
		}
		old = k
		return tx.Publish(ctx, EventKeyExpirySet, tenantID, k.Env,
			&eventsv1.ApiKeyExpirySet{KeyId: k.ID, ExpiresAt: timestamppb.New(exp)})
	})
	if err != nil {
		return domain.APIKey{}, "", domain.APIKey{}, err
	}
	return newKey, plain, old, nil
}

// RevokeKey revokes a key immediately; repeated calls are no-ops.
func (s *Service) RevokeKey(ctx context.Context, actor domain.Actor, tenantID, keyID string) (domain.APIKey, error) {
	var out domain.APIKey
	err := s.Repo.InTx(ctx, func(ctx context.Context, tx Tx) error {
		k, err := tx.GetKeyForUpdate(ctx, tenantID, keyID)
		if err != nil {
			return err
		}
		if err := domain.CanManage(actor, k.Env); err != nil {
			return err
		}
		out = k // already revoked: return as is
		now := s.Clock.Now()
		if !k.Revoke(now) {
			return nil
		}
		out = k
		if err := tx.UpdateKey(ctx, k); err != nil {
			return err
		}
		return tx.Publish(ctx, EventKeyRevoked, tenantID, k.Env,
			&eventsv1.ApiKeyRevoked{KeyId: k.ID, RevokedAt: timestamppb.New(now)})
	})
	if err != nil {
		return domain.APIKey{}, err
	}
	return out, nil
}

// ListKeys lists a tenant's keys in one environment. Any known role may list.
func (s *Service) ListKeys(ctx context.Context, actor domain.Actor, tenantID string, env domain.Environment) ([]domain.APIKey, error) {
	switch actor.Role {
	case domain.RoleOwner, domain.RoleAdmin, domain.RoleDeveloper, domain.RoleSupport:
	default:
		return nil, domain.ErrForbiddenRole
	}
	if !env.Valid() {
		return nil, fmt.Errorf("%w: environment", domain.ErrInvalidArgument)
	}
	var out []domain.APIKey
	err := s.Repo.InTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListKeys(ctx, tenantID, env)
		return err
	})
	return out, err
}

// insertKey generates, stores and announces a key inside tx.
func (s *Service) insertKey(ctx context.Context, tx Tx, tenantID, name string, env domain.Environment,
	scopes []string, ips []netip.Prefix, actor domain.Actor) (domain.APIKey, string, error) {
	plain, lookup, prefix, hash, err := s.Keys.Generate(env)
	if err != nil {
		return domain.APIKey{}, "", fmt.Errorf("generate key: %w", err)
	}
	k := domain.APIKey{
		ID: ids.New(ids.APIKey), TenantID: tenantID, Name: name, Lookup: lookup, DisplayPrefix: prefix,
		Env: env, Hash: hash, Scopes: scopes, AllowedIPs: ips, CreatedBy: actor, CreatedAt: s.Clock.Now(),
	}
	if err := tx.InsertKey(ctx, k); err != nil {
		return domain.APIKey{}, "", err
	}
	ipStrs := make([]string, len(ips))
	for i, p := range ips {
		ipStrs[i] = p.String()
	}
	var payload proto.Message = &eventsv1.ApiKeyCreated{
		KeyId: k.ID, TenantId: tenantID, Environment: protoEnv(env), Lookup: lookup,
		Hash: hash, Scopes: scopes, AllowedIps: ipStrs, CreatedAt: timestamppb.New(k.CreatedAt),
	}
	if err := tx.Publish(ctx, EventKeyCreated, tenantID, env, payload); err != nil {
		return domain.APIKey{}, "", err
	}
	return k, plain, nil
}

func protoEnv(e domain.Environment) eventsv1.Environment {
	if e == domain.EnvLive {
		return eventsv1.Environment_ENVIRONMENT_LIVE
	}
	return eventsv1.Environment_ENVIRONMENT_TEST
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: name is required", domain.ErrInvalidArgument)
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		return "", fmt.Errorf("%w: name longer than %d characters", domain.ErrInvalidArgument, maxNameLen)
	}
	return name, nil
}

// parseAllowedIPs accepts CIDRs and single addresses (/32 or /128), masked and deduped.
func parseAllowedIPs(in []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range in {
		s = strings.TrimSpace(s)
		var p netip.Prefix
		if strings.Contains(s, "/") {
			var err error
			if p, err = netip.ParsePrefix(s); err != nil {
				return nil, fmt.Errorf("%w: allowed ip %q", domain.ErrInvalidArgument, s)
			}
		} else {
			a, err := netip.ParseAddr(s)
			if err != nil {
				return nil, fmt.Errorf("%w: allowed ip %q", domain.ErrInvalidArgument, s)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = p.Masked()
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, nil
}
