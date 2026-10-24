package domain

import (
	"maps"
	"time"
)

// NewDeliveryInput is what a partner sends to create a delivery. A nil
// Model means the tenant default.
type NewDeliveryInput struct {
	ExternalID                                           string
	Model                                                *Model
	Pickup                                               Location
	PickupName                                           string
	Dropoff                                              Location
	DropoffLabel, CustomerFirstName, ItemsSummary, Notes *string
	ItemCount                                            *int
	Metadata                                             map[string]string
}

// Patch is a partial update. A nil outer pointer leaves the field
// unchanged; a nil inner pointer clears it. Metadata replaces the whole map
// when MetadataSet (a nil map clears it).
type Patch struct {
	Dropoff             *Location
	DropoffLabel, Notes **string
	Metadata            map[string]string
	MetadataSet         bool
}

// Delivery is the deliveries aggregate.
type Delivery struct {
	ID, TenantID, Env, ExternalID                        string
	Model                                                Model
	Status                                               Status
	Policy                                               PickupPolicy
	Pickup                                               Location
	PickupName                                           string
	Dropoff                                              Location
	DropoffLabel, CustomerFirstName, ItemsSummary, Notes *string
	ItemCount                                            *int
	Metadata                                             map[string]string
	Courier                                              *CourierRef
	// History is the full event log, oldest first. Report needs all of it
	// (duplicates, pickup confirmations, the conflict rule).
	History []HistoryEntry
	// DroppedReports counts noise reports not stored once the history held
	// MaxNoiseEntries of them. It is internal: no API exposes it.
	DroppedReports       int
	ErasedAt             *time.Time
	CreatedAt, UpdatedAt time.Time

	// newHistory holds the entries appended since load.
	newHistory []HistoryEntry
}

// History entry types other than the report types.
const (
	HistoryCreated   = "created"
	HistoryAssigned  = "assigned"
	HistoryCancelled = "cancelled"
)

// NewHistory returns the history entries appended since the delivery was
// loaded (or created), for the repository to insert.
func (d *Delivery) NewHistory() []HistoryEntry { return d.newHistory }

func (d *Delivery) record(h HistoryEntry) {
	d.History = append(d.History, h)
	d.newHistory = append(d.newHistory, h)
}

// NewDelivery validates in against the input limits and the tenant settings
// and returns a confirmed delivery.
func NewDelivery(id, tenantID, env string, in NewDeliveryInput, s TenantSettings, now time.Time) (*Delivery, []Event, error) {
	s = s.withDefaults()
	if err := validateNewDelivery(in); err != nil {
		return nil, nil, err
	}
	model := s.DefaultModel
	if in.Model != nil {
		model = *in.Model
		if !model.valid() {
			return nil, nil, invalid("delivery_model", "invalid", "must be direct or end_to_end")
		}
	}
	if !contains(s.AllowedModels, model) {
		return nil, nil, ErrModelNotAllowed
	}
	d := &Delivery{
		ID: id, TenantID: tenantID, Env: env, ExternalID: in.ExternalID,
		Model: model, Status: StatusConfirmed, Policy: s.PickupPolicy,
		Pickup: in.Pickup, PickupName: in.PickupName, Dropoff: in.Dropoff,
		DropoffLabel: in.DropoffLabel, CustomerFirstName: in.CustomerFirstName,
		ItemsSummary: in.ItemsSummary, Notes: in.Notes, ItemCount: in.ItemCount,
		Metadata:  maps.Clone(in.Metadata),
		CreatedAt: now, UpdatedAt: now,
	}
	d.record(HistoryEntry{
		Type: HistoryCreated, Actor: ActorPartner, To: StatusConfirmed,
		Outcome: OutcomeApplied, OccurredAt: now, ReceivedAt: now,
	})
	return d, []Event{{Type: EventDeliveryCreated, Payload: DeliveryCreated{
		DeliveryID: id, ExternalID: in.ExternalID, Model: model, Status: StatusConfirmed,
		Policy: s.PickupPolicy, Pickup: in.Pickup, PickupName: in.PickupName,
		Dropoff: in.Dropoff, CreatedAt: now,
	}}}, nil
}

func validateNewDelivery(in NewDeliveryInput) error {
	checks := []error{
		checkASCIIID("external_id", in.ExternalID),
		checkLocation("pickup.location", in.Pickup),
		checkLen("pickup.name", in.PickupName, maxPickupNameLen),
		checkLocation("dropoff.location", in.Dropoff),
		checkOptLen("dropoff.label", in.DropoffLabel, maxDropoffLabelLen),
		checkOptLen("customer.first_name", in.CustomerFirstName, maxFirstNameLen),
		checkOptLen("items_summary", in.ItemsSummary, maxItemsSummaryLen),
		checkOptLen("notes", in.Notes, maxNotesLen),
		checkMetadata(in.Metadata),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	if in.ItemCount != nil && (*in.ItemCount < 0 || *in.ItemCount > maxItemCount) {
		return invalid("item_count", "out_of_range", "must be between 0 and 999")
	}
	return checkDistance(in.Pickup, in.Dropoff)
}

// Assign attaches courier c. In end_to_end the first assignment moves
// confirmed → courier_assigned; otherwise the state is unchanged and a
// reassignment replaces the courier. From picked_up on it is
// ErrInvalidTransition.
func (d *Delivery) Assign(c CourierRef, at time.Time) ([]Event, error) {
	if c.ID == "" {
		return nil, invalid("courier_id", "required", "must not be empty")
	}
	if !contains(prePickup, d.Status) {
		return nil, ErrInvalidTransition
	}
	from, to := d.Status, d.Status
	if allowed(d.Model, ActorPartner, from, StatusCourierAssigned) {
		to = StatusCourierAssigned
	}
	cr := c
	d.Courier = &cr
	d.Status = to
	d.UpdatedAt = at
	outcome := OutcomeRecorded
	if to != from {
		outcome = OutcomeApplied
	}
	d.record(HistoryEntry{
		Type: HistoryAssigned, Actor: ActorPartner, From: from, To: to,
		Outcome: outcome, OccurredAt: at, ReceivedAt: at,
	})
	evs := []Event{{Type: EventCourierAssigned, Payload: CourierAssigned{DeliveryID: d.ID, CourierID: c.ID, AssignedAt: at}}}
	if to != from {
		evs = append(evs, d.statusChanged(from, ActorPartner, at, "", false))
	}
	return evs, nil
}

// Cancel moves a delivery that is not yet picked up to cancelled.
func (d *Delivery) Cancel(reason string, at time.Time) ([]Event, error) {
	if err := checkLen("reason", reason, maxReasonLen); err != nil {
		return nil, err
	}
	if !allowed(d.Model, ActorPartner, d.Status, StatusCancelled) {
		return nil, ErrInvalidTransition
	}
	from := d.Status
	d.Status = StatusCancelled
	d.UpdatedAt = at
	d.record(HistoryEntry{
		Type: HistoryCancelled, Actor: ActorPartner, From: from, To: StatusCancelled,
		Outcome: OutcomeApplied, OccurredAt: at, ReceivedAt: at, Reason: reason,
	})
	return []Event{d.statusChanged(from, ActorPartner, at, reason, false)}, nil
}

// Update applies p. The dropoff location is locked from picked_up on; the
// other fields change in any non-final state. Nothing changed means no
// event.
func (d *Delivery) Update(p Patch, at time.Time) ([]Event, error) {
	if d.Status.Final() {
		return nil, ErrInvalidTransition
	}
	dropoffChanged := p.Dropoff != nil && *p.Dropoff != d.Dropoff
	if dropoffChanged {
		if err := checkLocation("dropoff.location", *p.Dropoff); err != nil {
			return nil, err
		}
		if err := checkDistance(d.Pickup, *p.Dropoff); err != nil {
			return nil, err
		}
		if d.Status.rank() >= StatusPickedUp.rank() {
			return nil, ErrDropoffLocked
		}
	}
	if p.DropoffLabel != nil {
		if err := checkOptLen("dropoff.label", *p.DropoffLabel, maxDropoffLabelLen); err != nil {
			return nil, err
		}
	}
	if p.Notes != nil {
		if err := checkOptLen("notes", *p.Notes, maxNotesLen); err != nil {
			return nil, err
		}
	}
	if p.MetadataSet {
		if err := checkMetadata(p.Metadata); err != nil {
			return nil, err
		}
	}

	var changed []string
	ev := DeliveryUpdated{DeliveryID: d.ID}
	if dropoffChanged {
		d.Dropoff = *p.Dropoff
		loc := d.Dropoff
		ev.Dropoff = &loc
		changed = append(changed, "dropoff.location")
	}
	if p.DropoffLabel != nil && !equalOpt(d.DropoffLabel, *p.DropoffLabel) {
		d.DropoffLabel = cloneOpt(*p.DropoffLabel)
		changed = append(changed, "dropoff.label")
	}
	if p.Notes != nil && !equalOpt(d.Notes, *p.Notes) {
		d.Notes = cloneOpt(*p.Notes)
		changed = append(changed, "notes")
	}
	if p.MetadataSet && (!maps.Equal(d.Metadata, p.Metadata) || (d.Metadata == nil) != (p.Metadata == nil)) {
		d.Metadata = maps.Clone(p.Metadata)
		changed = append(changed, "metadata")
	}
	if len(changed) == 0 {
		return nil, nil
	}
	ev.ChangedFields = changed
	d.UpdatedAt = at
	return []Event{{Type: EventDeliveryUpdated, Payload: ev}}, nil
}

// ErasePersonalData clears the customer first name, notes, dropoff label and
// metadata of a finished delivery. A second call is a no-op.
func (d *Delivery) ErasePersonalData(at time.Time) ([]Event, error) {
	if !d.Status.Final() {
		return nil, ErrDeliveryActive
	}
	if d.ErasedAt != nil {
		return nil, nil
	}
	d.CustomerFirstName, d.Notes, d.DropoffLabel, d.Metadata = nil, nil, nil, nil
	erased := at
	d.ErasedAt = &erased
	d.UpdatedAt = at
	return []Event{{Type: EventPersonalDataErased, Payload: PersonalDataErased{DeliveryID: d.ID, ErasedAt: at}}}, nil
}

// statusChanged builds the delivery.status_changed event for a move from
// from to the current state.
func (d *Delivery) statusChanged(from Status, a ActorKind, at time.Time, reason string, overrode bool) Event {
	return Event{Type: EventStatusChanged, Payload: StatusChanged{
		DeliveryID: d.ID, From: from, To: d.Status, Actor: a,
		OccurredAt: at, Reason: reason, Overrode: overrode,
	}}
}

func equalOpt(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func cloneOpt(v *string) *string {
	if v == nil {
		return nil
	}
	s := *v
	return &s
}
