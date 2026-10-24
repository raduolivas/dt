package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func isInvalidTransition(err error) bool { return errors.Is(err, ErrInvalidTransition) }

// pickedUp returns a direct delivery picked up by the courier at t0+1m.
func pickedUp(t *testing.T) *Delivery {
	t.Helper()
	d := newDel(t, ModelDirect, PolicyAny)
	mustReport(t, d, rep("p1", ReportPickupConfirmed, ActorCourier, t0.Add(time.Minute)))
	return d
}

func TestReportDuplicateEventIDIsNoop(t *testing.T) {
	d := pickedUp(t)
	mustReport(t, d, rep("e1", ReportArriving, ActorCourier, t0.Add(5*time.Minute)))
	n := len(d.History)
	o, evs := mustReport(t, d, rep("e1", ReportDelivered, ActorPartner, t0.Add(6*time.Minute)))
	if o != OutcomeDuplicate || len(evs) != 0 || len(d.History) != n || d.Status != StatusArriving {
		t.Fatalf("outcome %s events %v history %d status %s", o, evs, len(d.History), d.Status)
	}
	// A duplicate pickup confirmation is also a no-op.
	o, evs = mustReport(t, d, rep("p1", ReportPickupConfirmed, ActorCourier, t0.Add(7*time.Minute)))
	if o != OutcomeDuplicate || len(evs) != 0 || len(d.History) != n {
		t.Fatalf("pickup duplicate: %s %v", o, evs)
	}
}

func TestReportAppliedEmitsStatusChanged(t *testing.T) {
	d := pickedUp(t)
	r := rep("e1", ReportArriving, ActorSystem, t0.Add(5*time.Minute))
	r.ReceivedAt = r.OccurredAt.Add(time.Second)
	o, evs := mustReport(t, d, r)
	if o != OutcomeApplied || len(evs) != 1 {
		t.Fatalf("outcome %s events %v", o, evs)
	}
	sc := evs[0].Payload.(StatusChanged)
	if sc.DeliveryID != "del_1" || sc.From != StatusPickedUp || sc.To != StatusArriving || sc.Actor != ActorSystem || sc.Overrode || !sc.OccurredAt.Equal(r.OccurredAt) {
		t.Fatalf("payload = %+v", sc)
	}
	if !d.UpdatedAt.Equal(r.ReceivedAt) {
		t.Fatalf("updated_at = %v", d.UpdatedAt)
	}
	h := d.History[len(d.History)-1]
	if h.EventID != "e1" || h.Type != string(ReportArriving) || h.From != StatusPickedUp || h.To != StatusArriving || h.Outcome != OutcomeApplied {
		t.Fatalf("history = %+v", h)
	}
}

func TestReportSameStateIsStale(t *testing.T) {
	d := pickedUp(t)
	mustReport(t, d, rep("e1", ReportArriving, ActorCourier, t0.Add(5*time.Minute)))
	o, evs := mustReport(t, d, rep("e2", ReportArriving, ActorCourier, t0.Add(6*time.Minute)))
	if o != OutcomeStale || len(evs) != 0 || d.History[len(d.History)-1].Outcome != OutcomeStale {
		t.Fatalf("outcome %s events %v", o, evs)
	}
}

func TestOutOfOrderOlderReportIsStale(t *testing.T) {
	d := pickedUp(t)
	at := t0.Add(10 * time.Minute)
	mustReport(t, d, rep("e1", ReportDelivered, ActorCourier, at))
	o, evs := mustReport(t, d, rep("e2", ReportArriving, ActorCourier, at.Add(-30*time.Second)))
	if o != OutcomeStale || len(evs) != 0 || d.Status != StatusDelivered {
		t.Fatalf("outcome %s events %v status %s", o, evs, d.Status)
	}
	h := d.History[len(d.History)-1]
	if h.EventID != "e2" || h.Outcome != OutcomeStale || h.From != StatusDelivered || h.To != StatusDelivered {
		t.Fatalf("history = %+v", h)
	}
}

func TestNewerBackwardReportIsInvalid(t *testing.T) {
	d := pickedUp(t)
	at := t0.Add(10 * time.Minute)
	mustReport(t, d, rep("e1", ReportDelivered, ActorCourier, at))
	n := len(d.History)
	_, _, err := d.Report(rep("e2", ReportArriving, ActorCourier, at.Add(30*time.Second)))
	if !isInvalidTransition(err) || d.Status != StatusDelivered || len(d.History) != n {
		t.Fatalf("err = %v status %s", err, d.Status)
	}
	// Behind a non-final state too.
	d = pickedUp(t)
	mustReport(t, d, rep("e1", ReportArriving, ActorCourier, at))
	if _, _, err := d.Report(rep("e2", ReportAtPickup, ActorCourier, at.Add(time.Minute))); !isInvalidTransition(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestConflictPartnerWins(t *testing.T) {
	d := pickedUp(t)
	at := t0.Add(10 * time.Minute)
	mustReport(t, d, rep("e1", ReportDelivered, ActorCourier, at))
	o, evs := mustReport(t, d, rep("e2", ReportFailed, ActorPartner, at.Add(20*time.Second)))
	if o != OutcomeApplied || d.Status != StatusFailed || len(evs) != 1 {
		t.Fatalf("outcome %s status %s events %v", o, d.Status, evs)
	}
	sc := evs[0].Payload.(StatusChanged)
	if !sc.Overrode || sc.From != StatusDelivered || sc.To != StatusFailed || sc.Actor != ActorPartner {
		t.Fatalf("payload = %+v", sc)
	}
}

func TestConflictCourierLoses(t *testing.T) {
	d := pickedUp(t)
	at := t0.Add(10 * time.Minute)
	mustReport(t, d, rep("e1", ReportFailed, ActorPartner, at))
	n := len(d.History)
	o, evs := mustReport(t, d, rep("e2", ReportDelivered, ActorCourier, at.Add(20*time.Second)))
	if o != OutcomeConflict || d.Status != StatusFailed || len(evs) != 0 || len(d.History) != n+1 {
		t.Fatalf("outcome %s status %s events %v", o, d.Status, evs)
	}
	if h := d.History[n]; h.Outcome != OutcomeConflict || h.To != StatusFailed {
		t.Fatalf("history = %+v", h)
	}
	// The window is symmetric: an older courier report also conflicts.
	o, _ = mustReport(t, d, rep("e3", ReportDelivered, ActorCourier, at.Add(-20*time.Second)))
	if o != OutcomeConflict {
		t.Fatalf("older courier report: %s", o)
	}
}

func TestConflictOutsideWindow(t *testing.T) {
	at := t0.Add(10 * time.Minute)

	d := pickedUp(t)
	mustReport(t, d, rep("e1", ReportDelivered, ActorCourier, at))
	if _, _, err := d.Report(rep("e2", ReportFailed, ActorPartner, at.Add(61*time.Second))); !isInvalidTransition(err) {
		t.Fatalf("partner at +61s: err = %v", err)
	}

	d = pickedUp(t)
	mustReport(t, d, rep("e1", ReportFailed, ActorPartner, at))
	if _, _, err := d.Report(rep("e2", ReportDelivered, ActorCourier, at.Add(61*time.Second))); !isInvalidTransition(err) {
		t.Fatalf("courier at +61s: err = %v", err)
	}

	// Exactly 60 s is inside the window.
	d = pickedUp(t)
	mustReport(t, d, rep("e1", ReportDelivered, ActorCourier, at))
	if o, _ := mustReport(t, d, rep("e2", ReportFailed, ActorPartner, at.Add(ConflictWindow))); o != OutcomeApplied || d.Status != StatusFailed {
		t.Fatalf("partner at +60s: %s %s", o, d.Status)
	}
}

func TestNoConflictWhenConsistent(t *testing.T) {
	// The partner reports at_pickup, the courier confirms pickup 10 s later:
	// normal processing.
	d := newDel(t, ModelDirect, PolicyAny)
	mustReport(t, d, rep("e1", ReportAtPickup, ActorPartner, t0.Add(time.Minute)))
	o, _ := mustReport(t, d, rep("e2", ReportPickupConfirmed, ActorCourier, t0.Add(time.Minute+10*time.Second)))
	if o != OutcomeApplied || d.Status != StatusPickedUp {
		t.Fatalf("outcome %s status %s", o, d.Status)
	}
	// The courier reports arriving right after a partner-driven pickup: normal.
	o, evs := mustReport(t, d, rep("e3", ReportArriving, ActorCourier, t0.Add(time.Minute+20*time.Second)))
	if o != OutcomeApplied || evs[0].Payload.(StatusChanged).Overrode {
		t.Fatalf("outcome %s", o)
	}
}

func TestReportValidation(t *testing.T) {
	tests := []struct {
		name  string
		mod   func(*Report)
		field string
	}{
		{"event_id empty", func(r *Report) { r.EventID = "" }, "event_id"},
		{"event_id 255", func(r *Report) { r.EventID = strings.Repeat("e", 255) }, ""},
		{"event_id 256", func(r *Report) { r.EventID = strings.Repeat("e", 256) }, "event_id"},
		{"event_id non-ascii", func(r *Report) { r.EventID = "é" }, "event_id"},
		{"type", func(r *Report) { r.Type = "teleported" }, "type"},
		{"actor", func(r *Report) { r.Actor = "customer" }, "actor"},
		{"reason 200", func(r *Report) { r.Reason = strings.Repeat("r", 200) }, ""},
		{"reason 201", func(r *Report) { r.Reason = strings.Repeat("r", 201) }, "reason"},
		{"by unknown", func(r *Report) { r.Type = ReportPickupConfirmed; r.By = "robot" }, "by"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := pickedUp(t)
			r := rep("e1", ReportArriving, ActorCourier, t0.Add(5*time.Minute))
			tc.mod(&r)
			_, _, err := d.Report(r)
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

func TestReportFutureOccurredAt(t *testing.T) {
	d := pickedUp(t)
	recv := t0.Add(5 * time.Minute)
	r := Report{EventID: "e1", Type: ReportArriving, Actor: ActorCourier, ReceivedAt: recv, OccurredAt: recv.Add(MaxFutureSkew + time.Second)}
	_, _, err := d.Report(r)
	wantValidation(t, err, "occurred_at", "in_future")

	r.OccurredAt = recv.Add(MaxFutureSkew)
	if _, _, err := d.Report(r); err != nil {
		t.Fatalf("5 min ahead: %v", err)
	}
}

func TestReportZeroOccurredAtMeansReceivedAt(t *testing.T) {
	d := pickedUp(t)
	recv := t0.Add(5 * time.Minute)
	_, evs := mustReport(t, d, Report{EventID: "e1", Type: ReportArriving, Actor: ActorCourier, ReceivedAt: recv})
	if !evs[0].Payload.(StatusChanged).OccurredAt.Equal(recv) || !d.History[len(d.History)-1].OccurredAt.Equal(recv) {
		t.Fatalf("occurred_at not defaulted: %+v", evs[0].Payload)
	}
}

func TestConflictOlderEarlierStateIsStale(t *testing.T) {
	d := pickedUp(t)
	at := t0.Add(10 * time.Minute)
	mustReport(t, d, rep("e1", ReportDelivered, ActorCourier, at))
	o, evs := mustReport(t, d, rep("e2", ReportArriving, ActorPartner, at.Add(-30*time.Second)))
	if o != OutcomeStale || len(evs) != 0 || d.Status != StatusDelivered {
		t.Fatalf("outcome %s events %v status %s", o, evs, d.Status)
	}
}

func TestConflictIgnoresOwnSide(t *testing.T) {
	// The courier picks up from confirmed, the partner then delivers: the
	// latest state change is the partner's own, so a partner at_pickup within
	// the window is not an override of the courier's pickup.
	d := newDel(t, ModelDirect, PolicyAny)
	at := t0.Add(10 * time.Minute)
	mustReport(t, d, rep("e1", ReportPickupConfirmed, ActorCourier, at))
	mustReport(t, d, rep("e2", ReportDelivered, ActorPartner, at.Add(10*time.Second)))
	n := len(d.History)
	_, evs, err := d.Report(rep("e3", ReportAtPickup, ActorPartner, at.Add(20*time.Second)))
	if !isInvalidTransition(err) || d.Status != StatusDelivered || len(evs) != 0 || len(d.History) != n {
		t.Fatalf("err %v status %s events %v", err, d.Status, evs)
	}
}
