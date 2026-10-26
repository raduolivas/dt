package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/api"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// deliveriesAPI serves the public delivery and courier routes over the
// deliveries internal RPC. The scope of every call (tenant, environment, actor)
// comes from the authenticated principal, never from the request.
type deliveriesAPI struct {
	c      deliveriesv1connect.DeliveriesServiceClient
	purger app.IdempotencyPurger // nil: no purge on erasure
	log    *slog.Logger
}

// Coordinates use the generated api.LatLng (float64, format: double in the
// contract). The response and request shapes below stay local where they
// differ from the generated models (nullable fields, internal mapping).

type deliveryJSON struct {
	ID            string `json:"id"`
	ExternalID    string `json:"external_id"`
	Environment   string `json:"environment"`
	DeliveryModel string `json:"delivery_model"`
	Status        string `json:"status"`
	Pickup        struct {
		Location api.LatLng `json:"location"`
		Name     string     `json:"name"`
	} `json:"pickup"`
	Dropoff struct {
		Location api.LatLng `json:"location"`
		Label    *string    `json:"label"`
	} `json:"dropoff"`
	Customer struct {
		FirstName *string `json:"first_name"`
	} `json:"customer"`
	ItemsSummary        *string               `json:"items_summary"`
	ItemCount           *int32                `json:"item_count"`
	Notes               *string               `json:"notes"`
	Metadata            map[string]string     `json:"metadata"`
	Courier             *courierRefJSON       `json:"courier"`
	PickupPolicy        string                `json:"pickup_policy"`
	PickupConfirmations []pickupConfirmationJ `json:"pickup_confirmations"`
	PersonalDataErased  bool                  `json:"personal_data_erased"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
	ReadyAt             *time.Time            `json:"ready_at"`
}

type courierRefJSON struct {
	ID          string `json:"id"`
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	Vehicle     string `json:"vehicle"`
}

type pickupConfirmationJ struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

func environmentName(e eventsv1.Environment) string {
	if e == eventsv1.Environment_ENVIRONMENT_LIVE {
		return string(domain.EnvLive)
	}
	return string(domain.EnvTest)
}

func toDeliveryJSON(d *deliveriesv1.Delivery) deliveryJSON {
	out := deliveryJSON{
		ID: d.GetId(), ExternalID: d.GetExternalId(), Environment: environmentName(d.GetEnvironment()),
		DeliveryModel: d.GetDeliveryModel(), Status: d.GetStatus(),
		ItemsSummary: d.ItemsSummary, ItemCount: d.ItemCount, Notes: d.Notes,
		Metadata: d.GetMetadata(), PickupPolicy: d.GetPickupPolicy(),
		PickupConfirmations: make([]pickupConfirmationJ, 0, len(d.GetPickupConfirmations())),
		PersonalDataErased:  d.GetPersonalDataErased(),
		CreatedAt:           d.GetCreatedAt().AsTime(), UpdatedAt: d.GetUpdatedAt().AsTime(),
	}
	if out.Metadata == nil {
		out.Metadata = map[string]string{}
	}
	if t := d.GetReadyAt(); t != nil {
		v := t.AsTime()
		out.ReadyAt = &v
	}
	out.Pickup.Name = d.GetPickup().GetName()
	out.Pickup.Location = api.LatLng{Lat: d.GetPickup().GetLocation().GetLat(), Lng: d.GetPickup().GetLocation().GetLng()}
	out.Dropoff.Location = api.LatLng{Lat: d.GetDropoff().GetLocation().GetLat(), Lng: d.GetDropoff().GetLocation().GetLng()}
	out.Dropoff.Label = d.GetDropoff().Label
	out.Customer.FirstName = d.GetCustomer().FirstName
	if c := d.GetCourier(); c != nil {
		out.Courier = &courierRefJSON{ID: c.GetId(), ExternalID: c.GetExternalId(), DisplayName: c.GetDisplayName(), Vehicle: c.GetVehicle()}
	}
	for _, pc := range d.GetPickupConfirmations() {
		out.PickupConfirmations = append(out.PickupConfirmations, pickupConfirmationJ{By: pc.GetBy(), At: pc.GetAt().AsTime()})
	}
	return out
}

func toLatLng(l api.LatLng) *eventsv1.LatLng { return &eventsv1.LatLng{Lat: l.Lat, Lng: l.Lng} }

// scopeFor is the RPC scope of the authenticated principal.
func scopeFor(w http.ResponseWriter, r *http.Request) (*deliveriesv1.Scope, bool) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		internalError(w, r) // Mount always authenticates first
		return nil, false
	}
	env := eventsv1.Environment_ENVIRONMENT_TEST
	if p.Env == domain.EnvLive {
		env = eventsv1.Environment_ENVIRONMENT_LIVE
	}
	return &deliveriesv1.Scope{TenantId: p.TenantID, Environment: env, ActorId: p.CredentialID}, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeBody decodes a JSON body into v. The OpenAPI validator already
// checked its shape, so a failure here is only a body that cannot be read. An
// empty body leaves v untouched.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil || errors.Is(err, io.EOF) {
		return true
	}
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Code: "validation_failed",
		Detail: "The request does not match the API contract.",
		Errors: []httpx.FieldError{{Field: "body", Code: "invalid", Message: "is not a valid request body"}}})
	return false
}

func (a *deliveriesAPI) create(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFor(w, r)
	if !ok {
		return
	}
	var in struct {
		ExternalID    string `json:"external_id"`
		DeliveryModel string `json:"delivery_model"`
		Pickup        struct {
			Location api.LatLng `json:"location"`
			Name     string     `json:"name"`
		} `json:"pickup"`
		Dropoff struct {
			Location api.LatLng `json:"location"`
			Label    *string    `json:"label"`
		} `json:"dropoff"`
		Customer struct {
			FirstName *string `json:"first_name"`
		} `json:"customer"`
		ItemsSummary *string           `json:"items_summary"`
		ItemCount    *int32            `json:"item_count"`
		Notes        *string           `json:"notes"`
		Metadata     map[string]string `json:"metadata"`
		ReadyAt      *time.Time        `json:"ready_at"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	var readyAt *timestamppb.Timestamp
	if in.ReadyAt != nil {
		readyAt = timestamppb.New(*in.ReadyAt)
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.CreateDeliveryResponse, error) {
		return a.c.CreateDelivery(ctx, &deliveriesv1.CreateDeliveryRequest{ReadyAt: readyAt,
			Scope: sc, ExternalId: in.ExternalID, DeliveryModel: in.DeliveryModel,
			Pickup:       &deliveriesv1.Pickup{Location: toLatLng(in.Pickup.Location), Name: in.Pickup.Name},
			Dropoff:      &deliveriesv1.Dropoff{Location: toLatLng(in.Dropoff.Location), Label: in.Dropoff.Label},
			Customer:     &deliveriesv1.Customer{FirstName: in.Customer.FirstName},
			ItemsSummary: in.ItemsSummary, ItemCount: in.ItemCount, Notes: in.Notes, Metadata: in.Metadata,
		})
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	SetAuditTarget(r, res.GetDelivery().GetId())
	writeJSON(w, http.StatusCreated, toDeliveryJSON(res.GetDelivery()))
}

func (a *deliveriesAPI) list(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	req := &deliveriesv1.ListDeliveriesRequest{
		Scope: sc, Status: q.Get("status"), DeliveryModel: q.Get("delivery_model"),
		ExternalId: q.Get("external_id"), StartingAfter: q.Get("starting_after"),
	}
	// The validator has already checked these.
	if v := q.Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			internalError(w, r)
			return
		}
		req.Limit = int32(n)
	}
	for name, dst := range map[string]**timestamppb.Timestamp{"created_gte": &req.CreatedGte, "created_lt": &req.CreatedLt} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				internalError(w, r)
				return
			}
			*dst = timestamppb.New(t)
		}
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.ListDeliveriesResponse, error) {
		return a.c.ListDeliveries(ctx, req)
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	out := struct {
		Data    []deliveryJSON `json:"data"`
		HasMore bool           `json:"has_more"`
	}{Data: make([]deliveryJSON, 0, len(res.GetData())), HasMore: res.GetHasMore()}
	for _, d := range res.GetData() {
		out.Data = append(out.Data, toDeliveryJSON(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *deliveriesAPI) get(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFor(w, r)
	if !ok {
		return
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.GetDeliveryResponse, error) {
		return a.c.GetDelivery(ctx, &deliveriesv1.GetDeliveryRequest{Scope: sc, Id: r.PathValue("id")})
	})
	a.reply(w, r, res.GetDelivery(), err)
}

// update serves PATCH. The generated model cannot tell JSON null from an absent
// key, so the body is read as raw members: a key that is present is masked,
// and null (or an empty metadata object) leaves its value unset, which the
// service reads as "clear"; an absent key is not masked.
func (a *deliveriesAPI) update(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFor(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !decodeBody(w, r, &raw) {
		return
	}
	req := &deliveriesv1.UpdateDeliveryRequest{Scope: sc, Id: r.PathValue("id")}
	var paths []string
	isNull := func(m json.RawMessage) bool { return string(m) == "null" }
	bad := func() {
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Code: "validation_failed"})
	}

	if m, present := raw["dropoff"]; present {
		var d struct {
			Location api.LatLng `json:"location"`
		}
		if err := json.Unmarshal(m, &d); err != nil {
			bad()
			return
		}
		paths = append(paths, "dropoff")
		req.Dropoff = toLatLng(d.Location)
	}
	for _, f := range []struct {
		key string
		dst **string
	}{{"dropoff_label", &req.DropoffLabel}, {"notes", &req.Notes}} {
		m, present := raw[f.key]
		if !present {
			continue
		}
		paths = append(paths, f.key)
		if isNull(m) {
			continue
		}
		var s string
		if err := json.Unmarshal(m, &s); err != nil {
			bad()
			return
		}
		*f.dst = &s
	}
	if m, present := raw["ready_at"]; present {
		paths = append(paths, "ready_at")
		if !isNull(m) {
			var t time.Time
			if err := json.Unmarshal(m, &t); err != nil {
				bad()
				return
			}
			req.ReadyAt = timestamppb.New(t)
		}
	}
	if m, present := raw["metadata"]; present {
		paths = append(paths, "metadata")
		if !isNull(m) {
			if err := json.Unmarshal(m, &req.Metadata); err != nil {
				bad()
				return
			}
		}
	}
	req.UpdateMask = &fieldmaskpb.FieldMask{Paths: paths}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.UpdateDeliveryResponse, error) {
		return a.c.UpdateDelivery(ctx, req)
	})
	a.reply(w, r, res.GetDelivery(), err)
}

func (a *deliveriesAPI) assign(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFor(w, r)
	if !ok {
		return
	}
	var in struct {
		CourierID string `json:"courier_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.AssignCourierResponse, error) {
		return a.c.AssignCourier(ctx, &deliveriesv1.AssignCourierRequest{Scope: sc, Id: r.PathValue("id"), CourierId: in.CourierID})
	})
	a.reply(w, r, res.GetDelivery(), err)
}

func (a *deliveriesAPI) reportEvent(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFor(w, r)
	if !ok {
		return
	}
	var in struct {
		EventID    string     `json:"event_id"`
		Type       string     `json:"type"`
		OccurredAt *time.Time `json:"occurred_at"`
		By         string     `json:"by"`
		Reason     string     `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	req := &deliveriesv1.ReportEventRequest{Scope: sc, Id: r.PathValue("id"), EventId: in.EventID, Type: in.Type, By: in.By, Reason: in.Reason}
	if in.OccurredAt != nil {
		req.OccurredAt = timestamppb.New(*in.OccurredAt)
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.ReportEventResponse, error) {
		return a.c.ReportEvent(ctx, req)
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Delivery deliveryJSON `json:"delivery"`
		Outcome  string       `json:"outcome"`
	}{toDeliveryJSON(res.GetDelivery()), res.GetOutcome()})
}

func (a *deliveriesAPI) cancel(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.CancelDeliveryResponse, error) {
		return a.c.CancelDelivery(ctx, &deliveriesv1.CancelDeliveryRequest{Scope: sc, Id: r.PathValue("id"), Reason: in.Reason})
	})
	a.reply(w, r, res.GetDelivery(), err)
}

func (a *deliveriesAPI) erasePersonalData(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFor(w, r)
	if !ok {
		return
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.ErasePersonalDataResponse, error) {
		return a.c.ErasePersonalData(ctx, &deliveriesv1.ErasePersonalDataRequest{Scope: sc, Id: r.PathValue("id")})
	})
	if err == nil {
		a.purgeStoredResponses(r, r.PathValue("id"))
	}
	a.reply(w, r, res.GetDelivery(), err)
}

// purgeTimeout bounds the idempotency purge after an erasure.
const purgeTimeout = 5 * time.Second

// purgeStoredResponses deletes the idempotency records of delivery id: their
// stored responses carry the personal data just erased. A failure is logged
// (no personal data) and does not fail the erasure; the records expire within
// 24 h anyway.
func (a *deliveriesAPI) purgeStoredResponses(r *http.Request, id string) {
	if a.purger == nil {
		return
	}
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), purgeTimeout)
	defer cancel()
	if _, err := a.purger.PurgeResource(ctx, p.TenantID, p.Env, id); err != nil {
		a.log.ErrorContext(r.Context(), "idempotency_purge_failed",
			"error", err.Error(),
			"tenant_id", p.TenantID,
			"delivery_id", id,
			"request_id", httpx.RequestIDFrom(r.Context()))
	}
}

// reply answers 200 with the delivery, or the mapped problem.
func (a *deliveriesAPI) reply(w http.ResponseWriter, r *http.Request, d *deliveriesv1.Delivery, err error) {
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toDeliveryJSON(d))
}
