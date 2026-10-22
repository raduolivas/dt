package domain

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func cloneDelivery(d *Delivery) *Delivery {
	c := *d
	c.History = slices.Clone(d.History)
	c.newHistory = slices.Clone(d.newHistory)
	c.Metadata = maps.Clone(d.Metadata)
	if d.Courier != nil {
		cr := *d.Courier
		c.Courier = &cr
	}
	return &c
}

func confirmations(d *Delivery) []ConfirmedBy {
	var out []ConfirmedBy
	for _, h := range d.History {
		if h.Type == string(ReportPickupConfirmed) {
			out = append(out, h.By)
		}
	}
	return out
}

func TestPropertyInvariants(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		model := rapid.SampledFrom(allModels).Draw(rt, "model")
		policy := rapid.SampledFrom([]PickupPolicy{PolicyAny, PolicyCourier, PolicyStore, PolicyCourierAndStore}).Draw(rt, "policy")
		in := validInput()
		in.Model = &model
		d, _, err := NewDelivery("del_1", "ten_1", "test", in, settingsWith(policy), t0)
		if err != nil {
			rt.Fatal(err)
		}
		clock := t0
		steps := rapid.IntRange(1, 40).Draw(rt, "steps")
		for i := range steps {
			clock = clock.Add(time.Duration(rapid.IntRange(0, 30).Draw(rt, "advance")) * time.Second)
			before := cloneDelivery(d)
			op := rapid.IntRange(0, 9).Draw(rt, "op")
			var (
				evs     []Event
				err     error
				outcome Outcome
				isRep   bool
				actor   ActorKind
			)
			switch {
			case op <= 6:
				isRep = true
				actor = rapid.SampledFrom(allActors).Draw(rt, "actor")
				r := Report{
					EventID:    fmt.Sprintf("e%d", rapid.IntRange(0, 15).Draw(rt, "event")),
					Type:       rapid.SampledFrom(allReportTypes).Draw(rt, "type"),
					Actor:      actor,
					By:         rapid.SampledFrom([]ConfirmedBy{"", ByStore, ByPartner, ByCourier}).Draw(rt, "by"),
					OccurredAt: clock.Add(time.Duration(rapid.IntRange(-120, 60).Draw(rt, "skew")) * time.Second),
					ReceivedAt: clock,
				}
				outcome, evs, err = d.Report(r)
			case op == 7:
				evs, err = d.Assign(CourierRef{ID: fmt.Sprintf("cou_%d", rapid.IntRange(0, 2).Draw(rt, "courier"))}, clock)
			case op == 8:
				evs, err = d.Cancel("", clock)
			default:
				loc := Location{Lat: customerLoc.Lat + float64(rapid.IntRange(0, 9).Draw(rt, "lat"))/1000, Lng: customerLoc.Lng}
				evs, err = d.Update(Patch{Dropoff: &loc, Notes: ptr(ptr("n"))}, clock)
			}

			if err != nil {
				if !reflect.DeepEqual(before, d) {
					rt.Fatalf("step %d: error %v mutated the delivery", i, err)
				}
				continue
			}

			// Every report adds exactly one history entry unless duplicate.
			if isRep {
				want := len(before.History) + 1
				if outcome == OutcomeDuplicate {
					want = len(before.History)
				}
				if len(d.History) != want {
					rt.Fatalf("step %d: %s report grew history %d → %d", i, outcome, len(before.History), len(d.History))
				}
			}

			// A final state only changes through the partner override.
			if before.Status.Final() && d.Status != before.Status {
				overrode := false
				for _, e := range evs {
					if sc, ok := e.Payload.(StatusChanged); ok && sc.Overrode {
						overrode = true
					}
				}
				if !isRep || actor != ActorPartner || !overrode {
					rt.Fatalf("step %d: final %s changed to %s without override", i, before.Status, d.Status)
				}
			}

			// picked_up is only reached when the policy is satisfied.
			if d.Status == StatusPickedUp && before.Status != StatusPickedUp && !PolicySatisfied(d.Policy, confirmations(d)) {
				rt.Fatalf("step %d: picked_up with confirmations %v under %s", i, confirmations(d), d.Policy)
			}

			// end_to_end never reaches at_pickup without a courier.
			if d.Model == ModelEndToEnd && d.Status == StatusAtPickup && d.Courier == nil {
				rt.Fatalf("step %d: end_to_end at_pickup without a courier", i)
			}

			// History and new history grow together.
			if len(d.NewHistory()) != len(d.History) {
				rt.Fatalf("step %d: new history %d, history %d", i, len(d.NewHistory()), len(d.History))
			}
		}
	})
}
