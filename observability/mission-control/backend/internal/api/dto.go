package api

import (
	"encoding/json"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

type eventDTO struct {
	ID         int64           `json:"id"`
	ObservedAt time.Time       `json:"observedAt"`
	OccurredAt time.Time       `json:"occurredAt"`
	Kind       string          `json:"kind"`
	Source     string          `json:"source"`
	Target     string          `json:"target"`
	Outcome    string          `json:"outcome"`
	Reason     string          `json:"reason"`
	TenantID   string          `json:"tenantId"`
	Env        string          `json:"environment"`
	DeliveryID string          `json:"deliveryId"`
	OrderRef   string          `json:"orderRef"`
	TraceID    string          `json:"traceId"`
	SpanID     string          `json:"spanId"`
	Origin     string          `json:"origin"`
	Attrs      json.RawMessage `json:"attrs"`
}

func toEventDTO(e store.Event) eventDTO {
	attrs := json.RawMessage(e.Attrs)
	if len(attrs) == 0 || !json.Valid(attrs) {
		attrs = json.RawMessage("{}")
	}
	return eventDTO{ID: e.ID, ObservedAt: e.ObservedAt, OccurredAt: e.OccurredAt, Kind: e.Kind, Source: e.Source,
		Target: e.Target, Outcome: e.Outcome, Reason: e.Reason, TenantID: e.TenantID, Env: e.Env,
		DeliveryID: e.DeliveryID, OrderRef: e.OrderRef, TraceID: e.TraceID, SpanID: e.SpanID, Origin: e.Origin, Attrs: attrs}
}

func toEventDTOs(evs []store.Event) []eventDTO {
	out := make([]eventDTO, len(evs))
	for i, e := range evs {
		out[i] = toEventDTO(e)
	}
	return out
}

type orderDTO struct {
	TenantID   string    `json:"tenantId"`
	Env        string    `json:"environment"`
	OrderRef   string    `json:"orderRef"`
	DeliveryID string    `json:"deliveryId"`
	Origin     string    `json:"origin"`
	CreatedAt  time.Time `json:"createdAt"`
}

func toOrderDTOs(hs []store.OrderHit) []orderDTO {
	out := make([]orderDTO, len(hs))
	for i, h := range hs {
		out[i] = orderDTO{h.TenantID, h.Environment, h.OrderRef, h.DeliveryID, h.Origin, h.CreatedAt}
	}
	return out
}
