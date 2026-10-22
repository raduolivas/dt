package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// Lisbon: the store and a customer about 1.6 km away.
var (
	storeLoc    = Location{Lat: 38.7223, Lng: -9.1393}
	customerLoc = Location{Lat: 38.7369, Lng: -9.1427}
)

func validInput() NewDeliveryInput {
	return NewDeliveryInput{
		ExternalID:        "order-1042",
		Pickup:            storeLoc,
		PickupName:        "Store",
		Dropoff:           customerLoc,
		DropoffLabel:      ptr("Home"),
		CustomerFirstName: ptr("Ana"),
		ItemsSummary:      ptr("2 pizzas"),
		ItemCount:         ptr(2),
		Notes:             ptr("ring twice"),
		Metadata:          map[string]string{"table": "12"},
	}
}

func settingsWith(policy PickupPolicy) TenantSettings {
	s := DefaultTenantSettings()
	s.PickupPolicy = policy
	return s
}

func newDel(t *testing.T, model Model, policy PickupPolicy) *Delivery {
	t.Helper()
	in := validInput()
	in.Model = &model
	d, _, err := NewDelivery("del_1", "ten_1", "test", in, settingsWith(policy), t0)
	if err != nil {
		t.Fatalf("NewDelivery: %v", err)
	}
	return d
}

func rep(id string, typ ReportType, actor ActorKind, at time.Time) Report {
	return Report{EventID: id, Type: typ, Actor: actor, OccurredAt: at, ReceivedAt: at}
}

func mustReport(t *testing.T, d *Delivery, r Report) (Outcome, []Event) {
	t.Helper()
	o, evs, err := d.Report(r)
	if err != nil {
		t.Fatalf("Report(%s %s by %s): %v", r.EventID, r.Type, r.Actor, err)
	}
	return o, evs
}

func eventTypes(evs []Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func wantValidation(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want ValidationError{%s}", err, field)
	}
	if ve.Field != field || (code != "" && ve.Code != code) {
		t.Fatalf("ValidationError = %+v, want field %q code %q", ve, field, code)
	}
}

func TestNewDeliveryCreates(t *testing.T) {
	d, evs, err := NewDelivery("del_1", "ten_1", "test", validInput(), DefaultTenantSettings(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusConfirmed || d.Model != ModelDirect || d.Policy != PolicyAny {
		t.Fatalf("delivery = %+v", d)
	}
	if !d.CreatedAt.Equal(t0) || !d.UpdatedAt.Equal(t0) {
		t.Fatalf("timestamps = %v %v", d.CreatedAt, d.UpdatedAt)
	}
	if got := eventTypes(evs); !reflect.DeepEqual(got, []string{EventDeliveryCreated}) {
		t.Fatalf("events = %v", got)
	}
	p := evs[0].Payload.(DeliveryCreated)
	if p.DeliveryID != "del_1" || p.ExternalID != "order-1042" || p.Status != StatusConfirmed || p.Dropoff != customerLoc {
		t.Fatalf("payload = %+v", p)
	}
	if len(d.NewHistory()) != 1 || d.NewHistory()[0].To != StatusConfirmed {
		t.Fatalf("new history = %+v", d.NewHistory())
	}
	// A delivery loaded by a repository has no new history.
	loaded := &Delivery{History: d.History}
	if len(loaded.NewHistory()) != 0 {
		t.Fatal("loaded delivery reports new history")
	}
}

func TestNewDeliveryModel(t *testing.T) {
	s := DefaultTenantSettings()
	s.AllowedModels = []Model{ModelDirect, ModelEndToEnd}
	s.DefaultModel = ModelEndToEnd

	in := validInput()
	d, _, err := NewDelivery("del_1", "ten_1", "test", in, s, t0)
	if err != nil || d.Model != ModelEndToEnd {
		t.Fatalf("nil model: %v %v", d, err)
	}

	in.Model = ptr(ModelDirect)
	d, _, err = NewDelivery("del_1", "ten_1", "test", in, s, t0)
	if err != nil || d.Model != ModelDirect {
		t.Fatalf("explicit model: %v %v", d, err)
	}

	s.AllowedModels = []Model{ModelEndToEnd}
	if _, _, err := NewDelivery("del_1", "ten_1", "test", in, s, t0); !errors.Is(err, ErrModelNotAllowed) {
		t.Fatalf("disallowed: err = %v", err)
	}

	in.Model = ptr(Model("drone"))
	_, _, err = NewDelivery("del_1", "ten_1", "test", in, s, t0)
	wantValidation(t, err, "delivery_model", "invalid")
}

func TestNewDeliveryValidation(t *testing.T) {
	far := Location{Lat: storeLoc.Lat + 0.9, Lng: storeLoc.Lng} // ~100.07 km north
	near := Location{Lat: storeLoc.Lat + 0.899, Lng: storeLoc.Lng}
	manyKeys := func(n int) map[string]string {
		m := make(map[string]string, n)
		for i := range n {
			m["k"+strings.Repeat("x", i%30)+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
		}
		return m
	}
	tests := []struct {
		name  string
		mod   func(*NewDeliveryInput)
		field string
		code  string
	}{
		{"external_id empty", func(in *NewDeliveryInput) { in.ExternalID = "" }, "external_id", "required"},
		{"external_id 255", func(in *NewDeliveryInput) { in.ExternalID = strings.Repeat("a", 255) }, "", ""},
		{"external_id 256", func(in *NewDeliveryInput) { in.ExternalID = strings.Repeat("a", 256) }, "external_id", "too_long"},
		{"external_id non-ascii", func(in *NewDeliveryInput) { in.ExternalID = "ordér" }, "external_id", "invalid"},
		{"external_id control", func(in *NewDeliveryInput) { in.ExternalID = "a\nb" }, "external_id", "invalid"},
		{"pickup.name 100", func(in *NewDeliveryInput) { in.PickupName = strings.Repeat("é", 100) }, "", ""},
		{"pickup.name 101", func(in *NewDeliveryInput) { in.PickupName = strings.Repeat("é", 101) }, "pickup.name", "too_long"},
		{"dropoff.label 100", func(in *NewDeliveryInput) { in.DropoffLabel = ptr(strings.Repeat("a", 100)) }, "", ""},
		{"dropoff.label 101", func(in *NewDeliveryInput) { in.DropoffLabel = ptr(strings.Repeat("a", 101)) }, "dropoff.label", "too_long"},
		{"first_name 50", func(in *NewDeliveryInput) { in.CustomerFirstName = ptr(strings.Repeat("a", 50)) }, "", ""},
		{"first_name 51", func(in *NewDeliveryInput) { in.CustomerFirstName = ptr(strings.Repeat("a", 51)) }, "customer.first_name", "too_long"},
		{"items_summary 200", func(in *NewDeliveryInput) { in.ItemsSummary = ptr(strings.Repeat("a", 200)) }, "", ""},
		{"items_summary 201", func(in *NewDeliveryInput) { in.ItemsSummary = ptr(strings.Repeat("a", 201)) }, "items_summary", "too_long"},
		{"item_count 0", func(in *NewDeliveryInput) { in.ItemCount = ptr(0) }, "", ""},
		{"item_count 999", func(in *NewDeliveryInput) { in.ItemCount = ptr(999) }, "", ""},
		{"item_count -1", func(in *NewDeliveryInput) { in.ItemCount = ptr(-1) }, "item_count", "out_of_range"},
		{"item_count 1000", func(in *NewDeliveryInput) { in.ItemCount = ptr(1000) }, "item_count", "out_of_range"},
		{"notes 500", func(in *NewDeliveryInput) { in.Notes = ptr(strings.Repeat("a", 500)) }, "", ""},
		{"notes 501", func(in *NewDeliveryInput) { in.Notes = ptr(strings.Repeat("a", 501)) }, "notes", "too_long"},
		{"metadata 50 keys", func(in *NewDeliveryInput) { in.Metadata = manyKeys(50) }, "", ""},
		{"metadata 51 keys", func(in *NewDeliveryInput) { in.Metadata = manyKeys(51) }, "metadata", "too_many"},
		{"metadata key 40", func(in *NewDeliveryInput) { in.Metadata = map[string]string{strings.Repeat("k", 40): "v"} }, "", ""},
		{"metadata key 41", func(in *NewDeliveryInput) { in.Metadata = map[string]string{strings.Repeat("k", 41): "v"} }, "metadata", "invalid_key"},
		{"metadata key empty", func(in *NewDeliveryInput) { in.Metadata = map[string]string{"": "v"} }, "metadata", "invalid_key"},
		{"metadata key charset", func(in *NewDeliveryInput) { in.Metadata = map[string]string{"a-b": "v"} }, "metadata", "invalid_key"},
		{"metadata value 500", func(in *NewDeliveryInput) { in.Metadata = map[string]string{"k": strings.Repeat("v", 500)} }, "", ""},
		{"metadata value 501", func(in *NewDeliveryInput) { in.Metadata = map[string]string{"k": strings.Repeat("v", 501)} }, "metadata", "too_long"},
		{"pickup lat 90", func(in *NewDeliveryInput) {
			in.Pickup = Location{Lat: 90, Lng: 0}
			in.Dropoff = Location{Lat: 89.5, Lng: 0}
		}, "", ""},
		{"pickup lat 90.1", func(in *NewDeliveryInput) { in.Pickup.Lat = 90.1 }, "pickup.location", "out_of_range"},
		{"dropoff lng -180.1", func(in *NewDeliveryInput) { in.Dropoff.Lng = -180.1 }, "dropoff.location", "out_of_range"},
		{"100 km apart", func(in *NewDeliveryInput) { in.Dropoff = near }, "", ""},
		{"over 100 km apart", func(in *NewDeliveryInput) { in.Dropoff = far }, "dropoff.location", "too_far"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mod(&in)
			_, _, err := NewDelivery("del_1", "ten_1", "test", in, DefaultTenantSettings(), t0)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			wantValidation(t, err, tc.field, tc.code)
		})
	}
}

func TestAssignRules(t *testing.T) {
	c1 := CourierRef{ID: "cou_1", ExternalID: "c1", DisplayName: "Rui", Vehicle: VehicleScooter}
	c2 := CourierRef{ID: "cou_2", ExternalID: "c2", DisplayName: "Eva", Vehicle: VehicleBicycle}

	t.Run("direct attaches without state change", func(t *testing.T) {
		d := newDel(t, ModelDirect, PolicyAny)
		evs, err := d.Assign(c1, t0.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != StatusConfirmed || d.Courier == nil || d.Courier.ID != "cou_1" {
			t.Fatalf("delivery = %+v", d)
		}
		if got := eventTypes(evs); !reflect.DeepEqual(got, []string{EventCourierAssigned}) {
			t.Fatalf("events = %v", got)
		}
	})

	t.Run("end_to_end moves to courier_assigned, reassign replaces", func(t *testing.T) {
		d := newDel(t, ModelEndToEnd, PolicyAny)
		evs, err := d.Assign(c1, t0.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != StatusCourierAssigned {
			t.Fatalf("status = %s", d.Status)
		}
		if got := eventTypes(evs); !reflect.DeepEqual(got, []string{EventCourierAssigned, EventStatusChanged}) {
			t.Fatalf("events = %v", got)
		}
		mustReport(t, d, rep("e1", ReportAtPickup, ActorCourier, t0.Add(2*time.Minute)))
		evs, err = d.Assign(c2, t0.Add(3*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != StatusAtPickup || d.Courier.ID != "cou_2" {
			t.Fatalf("after reassign: %s %+v", d.Status, d.Courier)
		}
		if got := eventTypes(evs); !reflect.DeepEqual(got, []string{EventCourierAssigned}) {
			t.Fatalf("events = %v", got)
		}
		p := evs[0].Payload.(CourierAssigned)
		if p.CourierID != "cou_2" || p.DeliveryID != "del_1" {
			t.Fatalf("payload = %+v", p)
		}
	})

	t.Run("refused from picked_up on", func(t *testing.T) {
		d := newDel(t, ModelDirect, PolicyAny)
		mustReport(t, d, rep("e1", ReportPickupConfirmed, ActorCourier, t0.Add(time.Minute)))
		if d.Status != StatusPickedUp {
			t.Fatalf("status = %s", d.Status)
		}
		if _, err := d.Assign(c1, t0.Add(2*time.Minute)); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("err = %v", err)
		}
		if d.Courier != nil {
			t.Fatal("courier attached after refusal")
		}
	})

	t.Run("refused when cancelled", func(t *testing.T) {
		d := newDel(t, ModelEndToEnd, PolicyAny)
		if _, err := d.Cancel("", t0); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Assign(c1, t0); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("courier id required", func(t *testing.T) {
		d := newDel(t, ModelDirect, PolicyAny)
		_, err := d.Assign(CourierRef{}, t0)
		wantValidation(t, err, "courier_id", "required")
	})
}

func TestCancelOnlyBeforePickup(t *testing.T) {
	for _, pre := range []Status{StatusConfirmed, StatusCourierAssigned, StatusAtPickup} {
		d := atState(ModelEndToEnd, pre, PolicyAny)
		evs, err := d.Cancel("customer changed mind", t0.Add(time.Hour))
		if err != nil {
			t.Fatalf("%s: %v", pre, err)
		}
		if d.Status != StatusCancelled {
			t.Fatalf("%s: status = %s", pre, d.Status)
		}
		p := evs[0].Payload.(StatusChanged)
		if p.From != pre || p.To != StatusCancelled || p.Actor != ActorPartner || p.Reason != "customer changed mind" {
			t.Fatalf("payload = %+v", p)
		}
	}
	for _, post := range []Status{StatusPickedUp, StatusArriving, StatusDelivered, StatusFailed, StatusCancelled} {
		d := atState(ModelDirect, post, PolicyAny)
		if _, err := d.Cancel("", t0.Add(time.Hour)); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("%s: err = %v", post, err)
		}
	}
	d := newDel(t, ModelDirect, PolicyAny)
	_, err := d.Cancel(strings.Repeat("r", 201), t0)
	wantValidation(t, err, "reason", "too_long")
	if _, err := d.Cancel(strings.Repeat("r", 200), t0); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateDropoffLockedAfterPickup(t *testing.T) {
	moved := Location{Lat: customerLoc.Lat + 0.001, Lng: customerLoc.Lng}

	d := atState(ModelDirect, StatusAtPickup, PolicyAny)
	evs, err := d.Update(Patch{Dropoff: &moved, Notes: ptr[*string](nil)}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	p := evs[0].Payload.(DeliveryUpdated)
	if evs[0].Type != EventDeliveryUpdated || !reflect.DeepEqual(p.ChangedFields, []string{"dropoff.location", "notes"}) || p.Dropoff == nil || *p.Dropoff != moved {
		t.Fatalf("event = %+v", evs[0])
	}
	if d.Notes != nil || d.Dropoff != moved {
		t.Fatalf("delivery = %+v", d)
	}

	for _, s := range []Status{StatusPickedUp, StatusArriving} {
		d := atState(ModelDirect, s, PolicyAny)
		if _, err := d.Update(Patch{Dropoff: &moved}, t0.Add(time.Hour)); !errors.Is(err, ErrDropoffLocked) {
			t.Fatalf("%s: err = %v", s, err)
		}
		// Other fields still change.
		evs, err := d.Update(Patch{DropoffLabel: ptr(ptr("Office")), Metadata: map[string]string{"a": "b"}, MetadataSet: true}, t0.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		p := evs[0].Payload.(DeliveryUpdated)
		if !reflect.DeepEqual(p.ChangedFields, []string{"dropoff.label", "metadata"}) || p.Dropoff != nil {
			t.Fatalf("payload = %+v", p)
		}
		// The same dropoff is not a change.
		evs, err = d.Update(Patch{Dropoff: &d.Dropoff}, t0.Add(time.Hour))
		if err != nil || len(evs) != 0 {
			t.Fatalf("same dropoff: %v %v", evs, err)
		}
	}

	for _, s := range []Status{StatusDelivered, StatusFailed, StatusCancelled} {
		d := atState(ModelDirect, s, PolicyAny)
		if _, err := d.Update(Patch{Notes: ptr(ptr("x"))}, t0.Add(time.Hour)); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("%s: err = %v", s, err)
		}
	}

	d = atState(ModelDirect, StatusConfirmed, PolicyAny)
	_, err = d.Update(Patch{Dropoff: &Location{Lat: 50, Lng: 0}}, t0)
	wantValidation(t, err, "dropoff.location", "too_far")
	_, err = d.Update(Patch{Metadata: map[string]string{"bad key": "v"}, MetadataSet: true}, t0)
	wantValidation(t, err, "metadata", "invalid_key")
	_, err = d.Update(Patch{Notes: ptr(ptr(strings.Repeat("n", 501)))}, t0)
	wantValidation(t, err, "notes", "too_long")
	_, err = d.Update(Patch{DropoffLabel: ptr(ptr(strings.Repeat("n", 101)))}, t0)
	wantValidation(t, err, "dropoff.label", "too_long")
	evs, err = d.Update(Patch{MetadataSet: true}, t0)
	if err != nil || d.Metadata != nil || evs[0].Payload.(DeliveryUpdated).ChangedFields[0] != "metadata" {
		t.Fatalf("clear metadata: %v %v %v", d.Metadata, evs, err)
	}
}

func TestEraseRequiresFinalAndIsIdempotent(t *testing.T) {
	for _, s := range []Status{StatusConfirmed, StatusCourierAssigned, StatusAtPickup, StatusPickedUp, StatusArriving} {
		d := atState(ModelEndToEnd, s, PolicyAny)
		if _, err := d.ErasePersonalData(t0); !errors.Is(err, ErrDeliveryActive) {
			t.Fatalf("%s: err = %v", s, err)
		}
	}
	d := atState(ModelDirect, StatusDelivered, PolicyAny)
	at := t0.Add(time.Hour)
	evs, err := d.ErasePersonalData(at)
	if err != nil {
		t.Fatal(err)
	}
	if d.CustomerFirstName != nil || d.Notes != nil || d.DropoffLabel != nil || d.Metadata != nil || d.ErasedAt == nil || !d.ErasedAt.Equal(at) {
		t.Fatalf("delivery = %+v", d)
	}
	if got := eventTypes(evs); !reflect.DeepEqual(got, []string{EventPersonalDataErased}) {
		t.Fatalf("events = %v", got)
	}
	evs, err = d.ErasePersonalData(at.Add(time.Hour))
	if err != nil || len(evs) != 0 || !d.ErasedAt.Equal(at) {
		t.Fatalf("second erase: %v %v %v", evs, err, d.ErasedAt)
	}
}

func TestNewCourier(t *testing.T) {
	in := CourierInput{ExternalID: "c-1", DisplayName: "Rui", PhotoURL: "https://cdn.example.com/rui.jpg", Vehicle: VehicleEBike}
	c, err := NewCourier("cou_1", in, t0)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "cou_1" || c.DisplayName != "Rui" || !c.CreatedAt.Equal(t0) || !c.UpdatedAt.Equal(t0) {
		t.Fatalf("courier = %+v", c)
	}
	e := c.UpsertedEvent()
	if e.Type != EventCourierUpserted || e.Payload.(CourierUpserted).CourierID != "cou_1" {
		t.Fatalf("event = %+v", e)
	}
	tests := []struct {
		name  string
		mod   func(*CourierInput)
		field string
	}{
		{"no photo", func(in *CourierInput) { in.PhotoURL = "" }, ""},
		{"name 50", func(in *CourierInput) { in.DisplayName = strings.Repeat("a", 50) }, ""},
		{"name 51", func(in *CourierInput) { in.DisplayName = strings.Repeat("a", 51) }, "display_name"},
		{"name empty", func(in *CourierInput) { in.DisplayName = "" }, "display_name"},
		{"http photo", func(in *CourierInput) { in.PhotoURL = "http://x.example/p.jpg" }, "photo_url"},
		{"photo no host", func(in *CourierInput) { in.PhotoURL = "https://" }, "photo_url"},
		{"photo whitespace", func(in *CourierInput) { in.PhotoURL = "https://x.example/a b.jpg" }, "photo_url"},
		{"photo 2048", func(in *CourierInput) { in.PhotoURL = "https://x.example/" + strings.Repeat("a", 2048-18) }, ""},
		{"photo 2049", func(in *CourierInput) { in.PhotoURL = "https://x.example/" + strings.Repeat("a", 2049-18) }, "photo_url"},
		{"vehicle", func(in *CourierInput) { in.Vehicle = "horse" }, "vehicle"},
		{"external id", func(in *CourierInput) { in.ExternalID = "" }, "external_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := in
			tc.mod(&in)
			_, err := NewCourier("cou_1", in, t0)
			if tc.field == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantValidation(t, err, tc.field, "")
		})
	}
}

func TestEventsNeverCarryPersonalData(t *testing.T) {
	forbidden := map[string]bool{"FirstName": true, "Notes": true, "Label": true, "Metadata": true}
	var walk func(t *testing.T, typ reflect.Type, path string, seen map[reflect.Type]bool)
	walk = func(t *testing.T, typ reflect.Type, path string, seen map[reflect.Type]bool) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
			if typ.Kind() == reflect.Map {
				walk(t, typ.Key(), path+"[key]", seen)
			}
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			f := typ.Field(i)
			for name := range forbidden {
				if strings.Contains(f.Name, name) {
					t.Errorf("%s.%s carries personal data", path, f.Name)
				}
			}
			walk(t, f.Type, path+"."+f.Name, seen)
		}
	}

	// Every payload type, plus the payloads actually emitted by a full lifecycle.
	payloads := []any{
		DeliveryCreated{}, StatusChanged{}, PickupConfirmed{}, PickupConfirmationPending{},
		DeliveryUpdated{}, PersonalDataErased{}, CourierAssigned{}, CourierUpserted{},
	}
	d := newDel(t, ModelEndToEnd, PolicyAny)
	var emitted []Event
	add := func(evs []Event, err error) {
		if err != nil {
			t.Fatal(err)
		}
		emitted = append(emitted, evs...)
	}
	_, evs, _ := NewDelivery("del_2", "ten_1", "test", validInput(), DefaultTenantSettings(), t0)
	emitted = append(emitted, evs...)
	add(d.Assign(CourierRef{ID: "cou_1"}, t0))
	add(d.Update(Patch{Notes: ptr(ptr("n")), DropoffLabel: ptr(ptr("l")), Metadata: map[string]string{"a": "b"}, MetadataSet: true}, t0))
	_, evs = mustReport(t, d, rep("e1", ReportPickupConfirmed, ActorCourier, t0))
	emitted = append(emitted, evs...)
	_, evs = mustReport(t, d, rep("e2", ReportDelivered, ActorCourier, t0))
	emitted = append(emitted, evs...)
	add(d.ErasePersonalData(t0))
	for _, e := range emitted {
		payloads = append(payloads, e.Payload)
	}
	seen := map[reflect.Type]bool{}
	for _, p := range payloads {
		typ := reflect.TypeOf(p)
		walk(t, typ, typ.Name(), seen)
	}
}
