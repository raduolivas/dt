// Package rpc serves the internal accounts Connect API
// (dt.accounts.v1.AccountsService) over the app use cases. Every RPC needs
// the internal bearer token; the API is never routed publicly.
package rpc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/timestamppb"

	accountsv1 "github.com/raduolivas/dt/backend/gen/dt/accounts/v1"
	"github.com/raduolivas/dt/backend/gen/dt/accounts/v1/accountsv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/accounts/internal/app"
	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
)

// readMaxBytes bounds each request message; requests here are tiny.
const readMaxBytes = 64 << 10

// Mount registers the AccountsService on mux, behind the token check.
func Mount(mux *http.ServeMux, svc *app.Service, token string, log *slog.Logger) error {
	if svc == nil || log == nil {
		return errors.New("rpc: service and logger are required")
	}
	auth, err := rpcauth.NewServerInterceptor(token)
	if err != nil {
		return err
	}
	srv := connect.NewServer(auth)
	accountsv1connect.RegisterAccountsServiceHandler(srv, &Server{svc: svc, log: log})
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(readMaxBytes))
	return nil
}

// Server implements accountsv1connect.AccountsServiceHandler.
type Server struct {
	svc *app.Service
	log *slog.Logger
}

var _ accountsv1connect.AccountsServiceHandler = (*Server)(nil)

func (s *Server) CreateTenant(ctx context.Context, req *accountsv1.CreateTenantRequest) (*accountsv1.CreateTenantResponse, error) {
	t, err := s.svc.CreateTenant(ctx, req.GetName())
	if err != nil {
		return nil, ToConnect(ctx, s.log, err)
	}
	s.log.InfoContext(ctx, "tenant_created", slog.String("tenant_id", t.ID))
	return &accountsv1.CreateTenantResponse{Tenant: &accountsv1.Tenant{
		Id: t.ID, Name: t.Name, LiveEnabled: t.LiveEnabled, CreatedAt: timestamppb.New(t.CreatedAt),
		Settings: toProtoSettings(t.Settings),
	}}, nil
}

func (s *Server) SetLiveEnabled(ctx context.Context, req *accountsv1.SetLiveEnabledRequest) (*accountsv1.SetLiveEnabledResponse, error) {
	if err := s.svc.SetLiveEnabled(ctx, req.GetTenantId(), req.GetEnabled()); err != nil {
		return nil, ToConnect(ctx, s.log, err)
	}
	s.log.InfoContext(ctx, "tenant_live_enabled_set",
		slog.String("tenant_id", req.GetTenantId()), slog.Bool("live_enabled", req.GetEnabled()))
	return &accountsv1.SetLiveEnabledResponse{}, nil
}

func (s *Server) CreateApiKey(ctx context.Context, req *accountsv1.CreateApiKeyRequest) (*accountsv1.CreateApiKeyResponse, error) {
	k, secret, err := s.svc.CreateKey(ctx, app.CreateKeyInput{
		Actor: toActor(req.GetActor()), TenantID: req.GetTenantId(), Name: req.GetName(),
		Env: toEnv(req.GetEnvironment()), Scopes: req.GetScopes(), AllowedIPs: req.GetAllowedIps(),
	})
	if err != nil {
		return nil, ToConnect(ctx, s.log, err)
	}
	s.logKey(ctx, "api_key_created", k)
	return &accountsv1.CreateApiKeyResponse{ApiKey: toProto(k), Secret: secret}, nil
}

func (s *Server) RollApiKey(ctx context.Context, req *accountsv1.RollApiKeyRequest) (*accountsv1.RollApiKeyResponse, error) {
	k, secret, old, err := s.svc.RollKey(ctx, toActor(req.GetActor()), req.GetTenantId(), req.GetKeyId())
	if err != nil {
		return nil, ToConnect(ctx, s.log, err)
	}
	s.logKey(ctx, "api_key_rolled", old)
	s.logKey(ctx, "api_key_created", k)
	return &accountsv1.RollApiKeyResponse{ApiKey: toProto(k), Secret: secret, Previous: toProto(old)}, nil
}

func (s *Server) RevokeApiKey(ctx context.Context, req *accountsv1.RevokeApiKeyRequest) (*accountsv1.RevokeApiKeyResponse, error) {
	k, err := s.svc.RevokeKey(ctx, toActor(req.GetActor()), req.GetTenantId(), req.GetKeyId())
	if err != nil {
		return nil, ToConnect(ctx, s.log, err)
	}
	s.logKey(ctx, "api_key_revoked", k)
	return &accountsv1.RevokeApiKeyResponse{ApiKey: toProto(k)}, nil
}

func (s *Server) ListApiKeys(ctx context.Context, req *accountsv1.ListApiKeysRequest) (*accountsv1.ListApiKeysResponse, error) {
	keys, err := s.svc.ListKeys(ctx, toActor(req.GetActor()), req.GetTenantId(), toEnv(req.GetEnvironment()))
	if err != nil {
		return nil, ToConnect(ctx, s.log, err)
	}
	out := make([]*accountsv1.ApiKey, len(keys))
	for i, k := range keys {
		out[i] = toProto(k)
	}
	return &accountsv1.ListApiKeysResponse{ApiKeys: out}, nil
}

func (s *Server) SetTenantSettings(ctx context.Context, req *accountsv1.SetTenantSettingsRequest) (*accountsv1.SetTenantSettingsResponse, error) {
	st, err := s.svc.SetTenantSettings(ctx, toActor(req.GetActor()), req.GetTenantId(), domain.TenantSettings{
		AllowedModels: req.GetAllowedModels(), DefaultModel: req.GetDefaultModel(),
		PickupConfirmation: req.GetPickupConfirmation(),
	})
	if err != nil {
		return nil, ToConnect(ctx, s.log, err)
	}
	s.log.InfoContext(ctx, "tenant_settings_set", slog.String("tenant_id", req.GetTenantId()))
	return &accountsv1.SetTenantSettingsResponse{Settings: toProtoSettings(st)}, nil
}

// logKey logs a key mutation by id only: never the secret, hash or lookup.
func (s *Server) logKey(ctx context.Context, msg string, k domain.APIKey) {
	s.log.InfoContext(ctx, msg, slog.String("credential_id", k.ID), slog.String("tenant_id", k.TenantID),
		slog.String("environment", string(k.Env)))
}

func toActor(a *accountsv1.Actor) domain.Actor {
	return domain.Actor{Type: a.GetType(), ID: a.GetId(), Role: domain.Role(a.GetRole())}
}

// toEnv maps the proto environment; UNSPECIFIED becomes an invalid
// environment that the use cases reject with ErrInvalidArgument.
func toEnv(e eventsv1.Environment) domain.Environment {
	switch e {
	case eventsv1.Environment_ENVIRONMENT_TEST:
		return domain.EnvTest
	case eventsv1.Environment_ENVIRONMENT_LIVE:
		return domain.EnvLive
	}
	return ""
}

func fromEnv(e domain.Environment) eventsv1.Environment {
	switch e {
	case domain.EnvTest:
		return eventsv1.Environment_ENVIRONMENT_TEST
	case domain.EnvLive:
		return eventsv1.Environment_ENVIRONMENT_LIVE
	}
	return eventsv1.Environment_ENVIRONMENT_UNSPECIFIED
}

// toProto copies a key's metadata. The hash and lookup are deliberately left out.
func toProto(k domain.APIKey) *accountsv1.ApiKey {
	ips := make([]string, len(k.AllowedIPs))
	for i, p := range k.AllowedIPs {
		ips[i] = p.String()
	}
	return &accountsv1.ApiKey{
		Id: k.ID, TenantId: k.TenantID, Environment: fromEnv(k.Env), Name: k.Name,
		DisplayPrefix: k.DisplayPrefix, Scopes: k.Scopes, AllowedIps: ips,
		CreatedAt: timestamppb.New(k.CreatedAt), ExpiresAt: ts(k.ExpiresAt), RevokedAt: ts(k.RevokedAt),
	}
}

func toProtoSettings(s domain.TenantSettings) *accountsv1.TenantSettings {
	return &accountsv1.TenantSettings{
		AllowedModels: s.AllowedModels, DefaultModel: s.DefaultModel,
		PickupConfirmation: s.PickupConfirmation, UpdatedAt: timestamppb.New(s.UpdatedAt),
	}
}

func ts(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}
