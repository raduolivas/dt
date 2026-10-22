package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// atState builds a delivery sitting in status s whose only history entry
// (by the partner) is an hour old, so the conflict rule never fires and
// newer reports are never stale.
func atState(m Model, s Status, policy PickupPolicy) *Delivery {
	d := &Delivery{
		ID: "del_1", TenantID: "ten_1", Env: "test", ExternalID: "order-1",
		Model: m, Status: s, Policy: policy,
		Pickup: storeLoc, PickupName: "Store", Dropoff: customerLoc,
		DropoffLabel: ptr("Home"), CustomerFirstName: ptr("Ana"), Notes: ptr("ring"),
		Metadata:  map[string]string{"k": "v"},
		CreatedAt: t0, UpdatedAt: t0,
		History: []HistoryEntry{{
			Type: HistoryCreated, Actor: ActorPartner, To: s, Outcome: OutcomeApplied,
			OccurredAt: t0, ReceivedAt: t0,
		}},
	}
	if s != StatusConfirmed {
		d.Courier = &CourierRef{ID: "cou_1"}
	}
	return d
}

var (
	allStatuses = []Status{StatusConfirmed, StatusCourierAssigned, StatusAtPickup, StatusPickedUp,
		StatusArriving, StatusDelivered, StatusCancelled, StatusFailed}
	allReportTypes = []ReportType{ReportAtPickup, ReportPickupConfirmed, ReportArriving, ReportDelivered, ReportFailed}
	allActors      = []ActorKind{ActorPartner, ActorCourier, ActorSystem}
	allModels      = []Model{ModelDirect, ModelEndToEnd}
)

// allowedReports is the transition table of spec §4.3 for reports, written
// out: "model actor from to". picked_up is reached via pickup_confirmed.
var allowedReports = func() map[string]bool {
	set := map[string]bool{}
	for _, m := range []string{"direct", "end_to_end"} {
		for _, a := range []string{"partner", "courier"} {
			for _, ft := range []string{
				"courier_assigned at_pickup",
				"courier_assigned picked_up",
				"at_pickup picked_up",
				"picked_up arriving",
				"picked_up delivered",
				"arriving delivered",
				"picked_up failed",
				"arriving failed",
			} {
				set[m+" "+a+" "+ft] = true
			}
		}
		set[m+" system picked_up arriving"] = true
	}
	for _, a := range []string{"partner", "courier"} {
		set["direct "+a+" confirmed at_pickup"] = true
		set["direct "+a+" confirmed picked_up"] = true
	}
	return set
}()

func TestTransitionTable(t *testing.T) {
	at := t0.Add(time.Hour)
	for _, m := range allModels {
		for _, s := range allStatuses {
			for _, rt := range allReportTypes {
				for _, a := range allActors {
					name := fmt.Sprintf("%s/%s/%s/%s", m, s, rt, a)
					t.Run(name, func(t *testing.T) {
						d := atState(m, s, PolicyAny)
						target := rt.target()
						var wantOutcome Outcome
						wantErr := false
						key := fmt.Sprintf("%s %s %s %s", m, a, s, target)
						switch {
						case rt == ReportPickupConfirmed:
							switch {
							case a == ActorSystem, s == StatusCancelled:
								wantErr = true
							case s.rank() >= StatusPickedUp.rank():
								wantOutcome = OutcomeRecorded
							case allowedReports[key]:
								wantOutcome = OutcomeApplied
							default:
								wantErr = true
							}
						case target == s:
							wantOutcome = OutcomeStale
						case s.Final() || target.rank() < s.rank():
							wantErr = true
						case allowedReports[key]:
							wantOutcome = OutcomeApplied
						default:
							wantErr = true
						}

						r := rep("e1", rt, a, at)
						if a == ActorCourier {
							r.By = ByCourier
						}
						before := len(d.History)
						o, evs, err := d.Report(r)
						if wantErr {
							if !errors.Is(err, ErrInvalidTransition) {
								t.Fatalf("err = %v, outcome %s; want ErrInvalidTransition", err, o)
							}
							if d.Status != s || len(d.History) != before || len(d.NewHistory()) != 0 {
								t.Fatal("error mutated the delivery")
							}
							return
						}
						if err != nil || o != wantOutcome {
							t.Fatalf("outcome = %s, err = %v; want %s", o, err, wantOutcome)
						}
						if len(d.History) != before+1 {
							t.Fatalf("history grew by %d", len(d.History)-before)
						}
						changed := false
						for _, e := range evs {
							if e.Type == EventStatusChanged {
								changed = true
							}
						}
						if wantOutcome == OutcomeApplied {
							wantTo := target
							if rt == ReportPickupConfirmed {
								wantTo = StatusPickedUp
							}
							if d.Status != wantTo || !changed {
								t.Fatalf("status = %s, events %v", d.Status, eventTypes(evs))
							}
						} else if d.Status != s || changed {
							t.Fatalf("status = %s, events %v", d.Status, eventTypes(evs))
						}
					})
				}
			}
		}
	}
}

func TestTransitionTableAssignAndCancel(t *testing.T) {
	for _, m := range allModels {
		for _, s := range allStatuses {
			if got, want := allowed(m, ActorPartner, s, StatusCancelled), s.rank() < StatusPickedUp.rank() && s != StatusCancelled; got != want {
				t.Errorf("%s %s → cancelled = %v", m, s, got)
			}
			if got, want := allowed(m, ActorPartner, s, StatusCourierAssigned), m == ModelEndToEnd && s == StatusConfirmed; got != want {
				t.Errorf("%s %s → courier_assigned = %v", m, s, got)
			}
			for _, a := range []ActorKind{ActorCourier, ActorSystem} {
				if allowed(m, a, s, StatusCancelled) || allowed(m, a, s, StatusCourierAssigned) {
					t.Errorf("%s %s %s may cancel or assign", m, a, s)
				}
			}
		}
	}
}

func TestStatusFinal(t *testing.T) {
	for _, s := range allStatuses {
		want := s == StatusDelivered || s == StatusCancelled || s == StatusFailed
		if s.Final() != want {
			t.Errorf("%s.Final() = %v", s, s.Final())
		}
	}
}
