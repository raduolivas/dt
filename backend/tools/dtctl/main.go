// Command dtctl is a development CLI for the accounts internal Connect RPC
// API: it creates tenants and manages their partner API keys. It is dev
// only; production uses the console (M7).
//
// Environment:
//
//	DT_ACCOUNTS_URL        accounts base URL (default http://127.0.0.1:8084)
//	DT_INTERNAL_RPC_TOKEN  the accounts INTERNAL_RPC_TOKEN (required)
//	DT_INTERNAL_RPC_TLS_CERT, _KEY, _CA  client certificate files for mTLS (all or none)
//
// A key's plaintext secret is printed once on stdout and nowhere else; all
// other output goes to stderr, except the data a command is asked for
// (tenant id, key list).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/timestamppb"

	accountsv1 "github.com/raduolivas/dt/backend/gen/dt/accounts/v1"
	"github.com/raduolivas/dt/backend/gen/dt/accounts/v1/accountsv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
)

const usage = `usage:
  dtctl tenant create --name NAME
  dtctl tenant live   --tenant ID --enabled=true|false
  dtctl tenant settings --tenant ID [--models direct,end_to_end] [--default direct] [--pickup any] [--prep-minutes N|none]
  dtctl key create    --tenant ID --env test|live [--name N] [--scope S]... [--allow-ip CIDR]...
  dtctl key roll      --tenant ID --key ID
  dtctl key revoke    --tenant ID --key ID
  dtctl key list      --tenant ID --env test|live

env: DT_ACCOUNTS_URL (default http://127.0.0.1:8084), DT_INTERNAL_RPC_TOKEN (required),
     DT_INTERNAL_RPC_TLS_CERT, DT_INTERNAL_RPC_TLS_KEY, DT_INTERNAL_RPC_TLS_CA (mTLS; use an https URL)
`

// actor is who dtctl acts as: the dev owner of every tenant.
var actor = &accountsv1.Actor{Type: "system", Id: "dtctl", Role: "owner"}

// errUsage marks a usage error; the usage text has already been printed.
var errUsage = errors.New("usage")

func main() {
	err := run(os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case err == nil:
	case errors.Is(err, errUsage):
		os.Exit(2)
	default:
		noteln(os.Stderr, "dtctl:", describe(err))
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		notef(stderr, "%s", usage)
		return errUsage
	}
	cmd, rest := args[0]+" "+args[1], args[2:]
	fs := flag.NewFlagSet("dtctl "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { notef(stderr, "%s", usage) }

	var (
		name, tenant, key, env string
		enabled                bool
		scopes, allowIPs       list
		settings               tenantSettings
	)
	switch cmd {
	case "tenant create":
		fs.StringVar(&name, "name", "", "tenant name")
	case "tenant live":
		fs.StringVar(&tenant, "tenant", "", "tenant id")
		fs.BoolVar(&enabled, "enabled", false, "allow live keys")
	case "tenant settings":
		fs.StringVar(&tenant, "tenant", "", "tenant id")
		fs.Var(&settings.models, "models", "comma-separated allowed models (default direct,end_to_end)")
		fs.StringVar(&settings.defaultModel, "default", "direct", "default model")
		fs.StringVar(&settings.pickup, "pickup", "any", "pickup confirmation: any, courier, store or courier_and_store")
		fs.Var(&settings.prep, "prep-minutes", "default preparation time, 1-240 minutes, or none (default none)")
	case "key create":
		fs.StringVar(&tenant, "tenant", "", "tenant id")
		fs.StringVar(&env, "env", "", "test or live")
		fs.StringVar(&name, "name", "dtctl", "key name")
		fs.Var(&scopes, "scope", "scope (repeatable; default all)")
		fs.Var(&allowIPs, "allow-ip", "allowed CIDR or address (repeatable; default any)")
	case "key roll", "key revoke":
		fs.StringVar(&tenant, "tenant", "", "tenant id")
		fs.StringVar(&key, "key", "", "key id")
	case "key list":
		fs.StringVar(&tenant, "tenant", "", "tenant id")
		fs.StringVar(&env, "env", "", "test or live")
	default:
		notef(stderr, "unknown command %q\n%s", cmd, usage)
		return errUsage
	}
	if err := fs.Parse(rest); err != nil {
		return errUsage
	}
	if fs.NArg() > 0 {
		notef(stderr, "unexpected arguments: %v\n", fs.Args())
		return errUsage
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	required := map[string]string{"tenant": tenant, "key": key, "env": env}
	for _, f := range []string{"tenant", "key", "env"} {
		if fs.Lookup(f) != nil && required[f] == "" {
			notef(stderr, "--%s is required\n", f)
			return errUsage
		}
	}
	if cmd == "tenant create" && name == "" {
		noteln(stderr, "--name is required")
		return errUsage
	}
	if cmd == "tenant live" && !set["enabled"] {
		noteln(stderr, "--enabled=true|false is required")
		return errUsage
	}
	var environment eventsv1.Environment
	if env != "" {
		var ok bool
		if environment, ok = parseEnv(env); !ok {
			notef(stderr, "--env must be test or live, not %q\n", env)
			return errUsage
		}
	}

	c, err := newClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx, info := connect.NewClientContext(ctx)
	if len(settings.models) == 0 {
		settings.models = csv{"direct", "end_to_end"}
	}
	err = call(ctx, c, cmd, stdout, stderr, name, tenant, key, environment, enabled, scopes, allowIPs, settings)
	if err != nil {
		return rpcError{err: err, dtCode: rpcauth.CodeFrom(info)}
	}
	return nil
}

func call(ctx context.Context, c accountsv1connect.AccountsServiceClient, cmd string, stdout, stderr io.Writer,
	name, tenant, key string, env eventsv1.Environment, enabled bool, scopes, allowIPs list,
	settings tenantSettings,
) error {
	switch cmd {
	case "tenant create":
		res, err := c.CreateTenant(ctx, &accountsv1.CreateTenantRequest{Name: name})
		if err != nil {
			return err
		}
		return out(stdout, res.GetTenant().GetId())
	case "tenant live":
		if _, err := c.SetLiveEnabled(ctx, &accountsv1.SetLiveEnabledRequest{TenantId: tenant, Enabled: enabled}); err != nil {
			return err
		}
		notef(stderr, "tenant %s live_enabled=%t\n", tenant, enabled)
	case "tenant settings":
		res, err := c.SetTenantSettings(ctx, &accountsv1.SetTenantSettingsRequest{
			Actor: actor, TenantId: tenant, AllowedModels: settings.models,
			DefaultModel: settings.defaultModel, PickupConfirmation: settings.pickup,
			DefaultPrepMinutes: settings.prep.v,
		})
		if err != nil {
			return err
		}
		st := res.GetSettings()
		notef(stderr, "tenant %s settings: models=%s default=%s pickup=%s prep_minutes=%s\n", tenant,
			strings.Join(st.GetAllowedModels(), ","), st.GetDefaultModel(), st.GetPickupConfirmation(),
			prepString(st.DefaultPrepMinutes))
	case "key create":
		res, err := c.CreateApiKey(ctx, &accountsv1.CreateApiKeyRequest{
			Actor: actor, TenantId: tenant, Environment: env, Name: name, Scopes: scopes, AllowedIps: allowIPs,
		})
		if err != nil {
			return err
		}
		noteln(stderr, "created", describeKey(res.GetApiKey()))
		noteln(stderr, "the secret below is shown once; store it now")
		return out(stdout, res.GetSecret())
	case "key roll":
		res, err := c.RollApiKey(ctx, &accountsv1.RollApiKeyRequest{Actor: actor, TenantId: tenant, KeyId: key})
		if err != nil {
			return err
		}
		noteln(stderr, "rolled  ", describeKey(res.GetPrevious()))
		noteln(stderr, "new     ", describeKey(res.GetApiKey()))
		noteln(stderr, "the secret below is shown once; store it now")
		return out(stdout, res.GetSecret())
	case "key revoke":
		res, err := c.RevokeApiKey(ctx, &accountsv1.RevokeApiKeyRequest{Actor: actor, TenantId: tenant, KeyId: key})
		if err != nil {
			return err
		}
		noteln(stderr, "revoked", describeKey(res.GetApiKey()))
	case "key list":
		res, err := c.ListApiKeys(ctx, &accountsv1.ListApiKeysRequest{Actor: actor, TenantId: tenant, Environment: env})
		if err != nil {
			return err
		}
		for _, k := range res.GetApiKeys() {
			if err := out(stdout, describeKey(k)); err != nil {
				return err
			}
		}
	}
	return nil
}

func newClient() (accountsv1connect.AccountsServiceClient, error) {
	token := os.Getenv("DT_INTERNAL_RPC_TOKEN")
	if token == "" {
		return nil, errors.New("DT_INTERNAL_RPC_TOKEN is not set")
	}
	base := os.Getenv("DT_ACCOUNTS_URL")
	if base == "" {
		base = "http://127.0.0.1:8084"
	}
	// DT_INTERNAL_RPC_TLS_CERT/_KEY/_CA select mTLS (then the URL is https).
	tlsCfg, err := rpcauth.ClientTLSFromEnv("DT_INTERNAL_RPC")
	if err != nil {
		return nil, err
	}
	c := &http.Client{Timeout: 15 * time.Second}
	if tlsCfg != nil {
		c.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}
	hc := bearer{c: c, value: "Bearer " + token}
	return accountsv1connect.NewAccountsServiceClient(connect.NewClient(connecthttp.NewTransport(hc, base))), nil
}

// bearer sets the internal RPC token on every request.
type bearer struct {
	c     *http.Client
	value string
}

func (b bearer) Do(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", b.value)
	return b.c.Do(r) //nolint:gosec // dev CLI: the URL is the operator's own DT_ACCOUNTS_URL

}

// rpcError carries the dt-code trailer next to the Connect error.
type rpcError struct {
	err    error
	dtCode string
}

func (e rpcError) Error() string { return e.err.Error() }
func (e rpcError) Unwrap() error { return e.err }

// describe renders an error as "<connect code>: <dt-code>", or with the error
// text when the server sent no dt-code (unauthenticated, transport errors).
func describe(err error) string {
	var re rpcError
	if !errors.As(err, &re) {
		return err.Error()
	}
	var ce *connect.Error
	if !errors.As(re.err, &ce) {
		return re.err.Error()
	}
	code := re.dtCode
	if code == "" {
		code = ce.Code().String()
	}
	if msg := ce.Message(); msg != "" && msg != code {
		return fmt.Sprintf("%s: %s (%s)", ce.Code(), code, msg)
	}
	return fmt.Sprintf("%s: %s", ce.Code(), code)
}

func describeKey(k *accountsv1.ApiKey) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s tenant=%s env=%s prefix=%s", k.GetId(), k.GetTenantId(), envName(k.GetEnvironment()), k.GetDisplayPrefix())
	if k.GetName() != "" {
		fmt.Fprintf(&b, " name=%q", k.GetName())
	}
	fmt.Fprintf(&b, " scopes=%s", strings.Join(k.GetScopes(), ","))
	if ips := k.GetAllowedIps(); len(ips) > 0 {
		fmt.Fprintf(&b, " allowed_ips=%s", strings.Join(ips, ","))
	}
	fmt.Fprintf(&b, " created_at=%s", ts(k.GetCreatedAt()))
	if k.GetExpiresAt() != nil {
		fmt.Fprintf(&b, " expires_at=%s", ts(k.GetExpiresAt()))
	}
	if k.GetRevokedAt() != nil {
		fmt.Fprintf(&b, " revoked_at=%s", ts(k.GetRevokedAt()))
	}
	return b.String()
}

func ts(t *timestamppb.Timestamp) string { return t.AsTime().UTC().Format(time.RFC3339) }

func parseEnv(s string) (eventsv1.Environment, bool) {
	switch s {
	case "test":
		return eventsv1.Environment_ENVIRONMENT_TEST, true
	case "live":
		return eventsv1.Environment_ENVIRONMENT_LIVE, true
	}
	return eventsv1.Environment_ENVIRONMENT_UNSPECIFIED, false
}

func envName(e eventsv1.Environment) string {
	switch e {
	case eventsv1.Environment_ENVIRONMENT_TEST:
		return "test"
	case eventsv1.Environment_ENVIRONMENT_LIVE:
		return "live"
	}
	return "unspecified"
}

// out writes a command's result to stdout; a failed write is an error, so a
// secret that never reached the caller is reported.
func out(w io.Writer, line string) error {
	if _, err := fmt.Fprintln(w, line); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}

// notef and noteln write diagnostics to stderr; there is nowhere to report a
// failed write.
func notef(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
func noteln(w io.Writer, a ...any)               { _, _ = fmt.Fprintln(w, a...) }

// list is a repeatable string flag.
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

// tenantSettings are the flags of `tenant settings`.
type tenantSettings struct {
	models               csv
	defaultModel, pickup string
	prep                 prepMinutes
}

// prepMinutes is the --prep-minutes flag: N in 1..240, or "none" (unset).
// The server validates the range.
type prepMinutes struct{ v *int32 }

func (p *prepMinutes) String() string { return prepString(p.v) }
func (p *prepMinutes) Set(s string) error {
	if s == "none" {
		p.v = nil
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return fmt.Errorf("want a number of minutes or none: %w", err)
	}
	v := int32(n)
	p.v = &v
	return nil
}

func prepString(p *int32) string {
	if p == nil {
		return "none"
	}
	return strconv.Itoa(int(*p))
}

// csv is a flag.Value that takes one comma-separated list.
type csv []string

func (c *csv) String() string { return strings.Join(*c, ",") }
func (c *csv) Set(v string) error {
	*c = strings.Split(v, ",")
	return nil
}
