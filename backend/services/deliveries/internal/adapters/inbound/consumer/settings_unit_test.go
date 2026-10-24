package consumer_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/inbound/consumer"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

// An invalid settings event is rejected as permanent (terminated, so it
// cannot block the ordered consumer), never applied, and logged with the
// tenant and event ids only.
func TestSettingsRejectsInvalidEvent(t *testing.T) {
	for name, ev := range map[string]*eventsv1.TenantSettingsSet{
		"unknown model":       {AllowedModels: []string{"direct", "drone"}, DefaultModel: "direct", PickupConfirmation: "any"},
		"default not allowed": {AllowedModels: []string{"direct"}, DefaultModel: "end_to_end", PickupConfirmation: "any"},
		"unknown policy":      {AllowedModels: []string{"direct"}, DefaultModel: "direct", PickupConfirmation: "secret-policy"},
		"no models":           {DefaultModel: "direct", PickupConfirmation: "any"},
	} {
		t.Run(name, func(t *testing.T) {
			ev.TenantId = "ten_bad"
			ev.UpdatedAt = timestamppb.Now()
			env, err := outbox.NewEnvelope("tenant.settings_set", "ten_bad", eventsv1.Environment_ENVIRONMENT_UNSPECIFIED,
				"accounts", nil, ev, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			applied := false
			h := consumer.Settings(func(context.Context, pgx.Tx, string, domain.TenantSettings, time.Time) error {
				applied = true
				return nil
			}, slog.New(slog.NewJSONHandler(&logs, nil)))

			err = h(context.Background(), nil, env)
			if !errors.Is(err, outbox.ErrPermanent) {
				t.Fatalf("handler = %v, want ErrPermanent", err)
			}
			if applied {
				t.Fatal("invalid settings were applied")
			}
			out := logs.String()
			if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, "ten_bad") || !strings.Contains(out, env.GetEventId()) {
				t.Fatalf("log does not name the tenant and event: %s", out)
			}
			for _, v := range []string{"drone", "secret-policy", "end_to_end"} {
				if strings.Contains(out, v) {
					t.Fatalf("log carries payload value %q: %s", v, out)
				}
			}
		})
	}
}
