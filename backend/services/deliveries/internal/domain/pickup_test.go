package domain

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestPolicySatisfied(t *testing.T) {
	c, s, p := ByCourier, ByStore, ByPartner
	tests := []struct {
		policy PickupPolicy
		by     []ConfirmedBy
		want   bool
	}{
		{PolicyAny, nil, false},
		{PolicyAny, []ConfirmedBy{p}, true},
		{PolicyAny, []ConfirmedBy{c}, true},
		{PolicyCourier, []ConfirmedBy{s, p}, false},
		{PolicyCourier, []ConfirmedBy{c}, true},
		{PolicyStore, []ConfirmedBy{c, p}, false},
		{PolicyStore, []ConfirmedBy{s}, true},
		{PolicyCourierAndStore, []ConfirmedBy{c, c}, false},
		{PolicyCourierAndStore, []ConfirmedBy{s, p}, false},
		{PolicyCourierAndStore, []ConfirmedBy{s, c}, true},
		{PickupPolicy("bogus"), []ConfirmedBy{c, s, p}, false},
	}
	for _, tc := range tests {
		if got := PolicySatisfied(tc.policy, tc.by); got != tc.want {
			t.Errorf("PolicySatisfied(%s, %v) = %v", tc.policy, tc.by, got)
		}
	}
}

// sequences returns every confirmation sequence of length 0..n.
func sequences(n int) [][]ConfirmedBy {
	out := [][]ConfirmedBy{nil}
	prev := [][]ConfirmedBy{nil}
	for range n {
		var next [][]ConfirmedBy
		for _, s := range prev {
			for _, b := range []ConfirmedBy{ByCourier, ByStore, ByPartner} {
				next = append(next, append(slices.Clone(s), b))
			}
		}
		out = append(out, next...)
		prev = next
	}
	return out
}

func TestPickupPolicies(t *testing.T) {
	for _, policy := range []PickupPolicy{PolicyAny, PolicyCourier, PolicyStore, PolicyCourierAndStore} {
		for _, seq := range sequences(3) {
			t.Run(fmt.Sprintf("%s/%v", policy, seq), func(t *testing.T) {
				d := atState(ModelDirect, StatusAtPickup, policy)
				satisfiedAt := -1
				for i := range seq {
					if PolicySatisfied(policy, seq[:i+1]) {
						satisfiedAt = i
						break
					}
				}
				for i, by := range seq {
					at := t0.Add(time.Hour + time.Duration(i)*time.Minute)
					r := rep(fmt.Sprintf("e%d", i), ReportPickupConfirmed, ActorPartner, at)
					if by == ByCourier {
						r.Actor = ActorCourier
					} else {
						r.By = by
					}
					o, evs := mustReport(t, d, r)
					wantTypes := []string{EventPickupConfirmed}
					want := OutcomeRecorded
					if i == satisfiedAt {
						want = OutcomeApplied
						wantTypes = append(wantTypes, EventStatusChanged)
					}
					if o != want || !reflect.DeepEqual(eventTypes(evs), wantTypes) {
						t.Fatalf("step %d (%s): outcome %s events %v; want %s %v", i, by, o, eventTypes(evs), want, wantTypes)
					}
					if pc := evs[0].Payload.(PickupConfirmed); pc.By != by || !pc.OccurredAt.Equal(at) {
						t.Fatalf("payload = %+v", pc)
					}
					wantStatus := StatusAtPickup
					if satisfiedAt >= 0 && i >= satisfiedAt {
						wantStatus = StatusPickedUp
					}
					if d.Status != wantStatus {
						t.Fatalf("step %d: status %s, want %s", i, d.Status, wantStatus)
					}
				}
			})
		}
	}
}

func TestPickupConfirmedBy(t *testing.T) {
	d := atState(ModelDirect, StatusAtPickup, PolicyStore)
	// The partner defaults to partner.
	_, evs := mustReport(t, d, rep("e1", ReportPickupConfirmed, ActorPartner, t0.Add(time.Hour)))
	if evs[0].Payload.(PickupConfirmed).By != ByPartner {
		t.Fatalf("by = %v", evs[0].Payload)
	}
	// A partner may not claim the courier confirmed; a courier is always courier.
	r := rep("e2", ReportPickupConfirmed, ActorPartner, t0.Add(time.Hour))
	r.By = ByCourier
	_, _, err := d.Report(r)
	wantValidation(t, err, "by", "invalid")
	r = rep("e3", ReportPickupConfirmed, ActorCourier, t0.Add(time.Hour))
	r.By = ByStore
	_, _, err = d.Report(r)
	wantValidation(t, err, "by", "invalid")
	_, evs = mustReport(t, d, rep("e4", ReportPickupConfirmed, ActorCourier, t0.Add(time.Hour)))
	if evs[0].Payload.(PickupConfirmed).By != ByCourier {
		t.Fatalf("by = %v", evs[0].Payload)
	}
	// "by" is ignored on other report types.
	r = rep("e5", ReportDelivered, ActorPartner, t0.Add(time.Hour))
	r.By = ByStore
	if _, _, err := d.Report(r); err == nil || !isInvalidTransition(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestPickupConfirmedAfterPickupIsRecorded(t *testing.T) {
	d := newDel(t, ModelDirect, PolicyAny)
	mustReport(t, d, rep("e1", ReportPickupConfirmed, ActorCourier, t0.Add(time.Minute)))
	mustReport(t, d, rep("e2", ReportDelivered, ActorCourier, t0.Add(10*time.Minute)))
	o, evs := mustReport(t, d, rep("e3", ReportPickupConfirmed, ActorPartner, t0.Add(11*time.Minute)))
	if o != OutcomeRecorded || !reflect.DeepEqual(eventTypes(evs), []string{EventPickupConfirmed}) || d.Status != StatusDelivered {
		t.Fatalf("outcome %s events %v status %s", o, eventTypes(evs), d.Status)
	}
}
