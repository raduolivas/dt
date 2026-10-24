package domain

import "time"

// Report is a status report from the partner, the courier device or DT.
// A zero OccurredAt means ReceivedAt. By is only meaningful for
// pickup_confirmed: a partner's defaults to partner and may be store; a
// courier's is always courier.
type Report struct {
	EventID                string
	Type                   ReportType
	Actor                  ActorKind
	By                     ConfirmedBy
	OccurredAt, ReceivedAt time.Time
	Reason                 string
}

// HistoryEntry is one line of a delivery's event log. Type is a ReportType
// or one of the History* constants. From and To are the states before and
// after; they are equal when the state did not change.
type HistoryEntry struct {
	EventID                string
	Type                   string
	Actor                  ActorKind
	By                     ConfirmedBy
	From, To               Status
	Outcome                Outcome
	OccurredAt, ReceivedAt time.Time
	Reason                 string
}

// Report applies r (spec §4.3–4.4). The checks run in order: duplicate,
// pickup confirmation, conflict rule, same state (stale), a non-partner
// repeating a state it already reached after the other side changed it
// within ConflictWindow (conflict, OQ18), backwards or
// final (stale if older than the last applied entry, else
// ErrInvalidTransition), then the transition table. An error leaves the
// delivery untouched; any other outcome but duplicate adds one history
// entry.
func (d *Delivery) Report(r Report) (Outcome, []Event, error) {
	r, err := normalizeReport(r)
	if err != nil {
		return "", nil, err
	}
	if d.hasEvent(r.EventID) {
		return OutcomeDuplicate, nil, nil
	}
	if r.Type == ReportPickupConfirmed {
		return d.reportPickup(r)
	}

	target := r.Type.target()
	if d.conflicts(r, target) {
		if r.Actor != ActorPartner {
			d.recordReport(r, d.Status, OutcomeConflict)
			return OutcomeConflict, nil, nil
		}
		return d.apply(r, target, true)
	}
	if target == d.Status {
		d.recordReport(r, d.Status, OutcomeStale)
		return OutcomeStale, nil, nil
	}
	if d.repeatsAfterOverride(r, target) {
		d.recordReport(r, d.Status, OutcomeConflict)
		return OutcomeConflict, nil, nil
	}
	if d.Status.Final() || target.rank() < d.Status.rank() {
		if last := d.lastApplied(func(HistoryEntry) bool { return true }); last != nil && r.OccurredAt.Before(last.OccurredAt) {
			d.recordReport(r, d.Status, OutcomeStale)
			return OutcomeStale, nil, nil
		}
		return "", nil, ErrInvalidTransition
	}
	if !allowed(d.Model, r.Actor, d.Status, target) {
		return "", nil, ErrInvalidTransition
	}
	return d.apply(r, target, false)
}

// reportPickup handles pickup_confirmed: it skips the conflict and stale
// steps. Every confirmation is recorded and broadcast; the state moves to
// picked_up once the delivery's policy is satisfied.
func (d *Delivery) reportPickup(r Report) (Outcome, []Event, error) {
	if r.Actor == ActorSystem || d.Status == StatusCancelled {
		return "", nil, ErrInvalidTransition
	}
	confirmed := Event{Type: EventPickupConfirmed, Payload: PickupConfirmed{DeliveryID: d.ID, By: r.By, OccurredAt: r.OccurredAt}}
	if d.Status.rank() >= StatusPickedUp.rank() {
		d.recordReport(r, d.Status, OutcomeRecorded)
		return OutcomeRecorded, []Event{confirmed}, nil
	}
	if !allowed(d.Model, r.Actor, d.Status, StatusPickedUp) {
		return "", nil, ErrInvalidTransition
	}
	if !PolicySatisfied(d.Policy, append(d.confirmations(), r.By)) {
		d.recordReport(r, d.Status, OutcomeRecorded)
		return OutcomeRecorded, []Event{confirmed}, nil
	}
	_, evs, err := d.apply(r, StatusPickedUp, false)
	return OutcomeApplied, append([]Event{confirmed}, evs...), err
}

// conflicts implements the conflict rule (decision C4, rulings R1/R5): the
// latest applied state change L was made by the other side within
// ConflictWindow, r does not report an earlier state than L reached, and
// r's target is not reachable from the current state but was reachable
// from the state L moved from. A late report of an earlier state is never a
// conflict; it falls through to the stale/invalid steps.
func (d *Delivery) conflicts(r Report, target Status) bool {
	last := d.lastApplied(func(HistoryEntry) bool { return true })
	if last == nil || !last.Actor.opposes(r.Actor) || target == d.Status {
		return false
	}
	if target.rank() < last.To.rank() {
		return false
	}
	if delta := r.OccurredAt.Sub(last.OccurredAt).Abs(); delta > ConflictWindow {
		return false
	}
	if !d.Status.Final() && allowed(d.Model, r.Actor, d.Status, target) {
		return false
	}
	return allowed(d.Model, r.Actor, last.From, target)
}

// repeatsAfterOverride reports whether a non-partner r re-reports a state
// its own side already reached, after the latest applied state change by
// the opposing side within ConflictWindow of r (OQ18).
func (d *Delivery) repeatsAfterOverride(r Report, target Status) bool {
	if r.Actor == ActorPartner {
		return false
	}
	last := d.lastApplied(func(HistoryEntry) bool { return true })
	if last == nil || !last.Actor.opposes(r.Actor) || r.OccurredAt.Sub(last.OccurredAt).Abs() > ConflictWindow {
		return false
	}
	return d.lastApplied(func(h HistoryEntry) bool { return h.Actor == r.Actor && h.To == target }) != nil
}

// apply moves the delivery to target and records r as applied.
func (d *Delivery) apply(r Report, target Status, overrode bool) (Outcome, []Event, error) {
	from := d.Status
	d.Status = target
	d.recordReport(r, from, OutcomeApplied)
	return OutcomeApplied, []Event{d.statusChanged(from, r.Actor, r.OccurredAt, r.Reason, overrode)}, nil
}

// recordReport appends r to the history with the state it found (from) and
// the current state.
func (d *Delivery) recordReport(r Report, from Status, o Outcome) {
	d.record(HistoryEntry{
		EventID: r.EventID, Type: string(r.Type), Actor: r.Actor, By: r.By,
		From: from, To: d.Status, Outcome: o,
		OccurredAt: r.OccurredAt, ReceivedAt: r.ReceivedAt, Reason: r.Reason,
	})
	if !r.ReceivedAt.IsZero() {
		d.UpdatedAt = r.ReceivedAt
	}
}

// lastApplied returns the latest history entry that changed the state and
// matches keep.
func (d *Delivery) lastApplied(keep func(HistoryEntry) bool) *HistoryEntry {
	for i := len(d.History) - 1; i >= 0; i-- {
		h := &d.History[i]
		if h.Outcome == OutcomeApplied && h.From != h.To && keep(*h) {
			return h
		}
	}
	return nil
}

func (d *Delivery) hasEvent(id string) bool {
	for _, h := range d.History {
		if h.EventID == id {
			return true
		}
	}
	return false
}

// confirmations returns who confirmed the pickup so far.
func (d *Delivery) confirmations() []ConfirmedBy {
	var out []ConfirmedBy
	for _, h := range d.History {
		if h.Type == string(ReportPickupConfirmed) {
			out = append(out, h.By)
		}
	}
	return out
}

// normalizeReport checks r against the input limits and fills defaults.
func normalizeReport(r Report) (Report, error) {
	if err := checkASCIIID("event_id", r.EventID); err != nil {
		return r, err
	}
	if r.Type.target() == "" {
		return r, invalid("type", "invalid", "must be at_pickup, pickup_confirmed, arriving, delivered or failed")
	}
	if !r.Actor.valid() {
		return r, invalid("actor", "invalid", "must be partner, courier or system")
	}
	if err := checkLen("reason", r.Reason, maxReasonLen); err != nil {
		return r, err
	}
	if r.OccurredAt.IsZero() {
		r.OccurredAt = r.ReceivedAt
	}
	if !r.ReceivedAt.IsZero() && r.OccurredAt.After(r.ReceivedAt.Add(MaxFutureSkew)) {
		return r, invalid("occurred_at", "in_future", "must be at most 5 minutes after receipt")
	}
	if r.Type != ReportPickupConfirmed {
		r.By = ""
		return r, nil
	}
	switch r.Actor {
	case ActorCourier:
		if r.By != "" && r.By != ByCourier {
			return r, invalid("by", "invalid", "a courier confirms as courier")
		}
		r.By = ByCourier
	case ActorPartner:
		if r.By == "" {
			r.By = ByPartner
		}
		if r.By != ByPartner && r.By != ByStore {
			return r, invalid("by", "invalid", "must be partner or store")
		}
	}
	return r, nil
}
