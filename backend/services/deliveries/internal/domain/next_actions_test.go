package domain

import (
	"reflect"
	"testing"
)

func TestNextActions(t *testing.T) {
	t.Parallel()
	courier := []PickupConfirmation{{By: ByCourier}}
	store := []PickupConfirmation{{By: ByStore}}
	tests := []struct {
		name   string
		model  Model
		status Status
		policy PickupPolicy
		confs  []PickupConfirmation
		want   []string
	}{
		{"direct confirmed", ModelDirect, StatusConfirmed, PolicyAny, nil, []string{"at_pickup", "pickup_confirmed"}},
		{"direct confirmed courier_and_store", ModelDirect, StatusConfirmed, PolicyCourierAndStore, nil, []string{"at_pickup", "pickup_confirmed"}},
		{"end_to_end confirmed", ModelEndToEnd, StatusConfirmed, PolicyAny, nil, []string{}},
		{"end_to_end assigned", ModelEndToEnd, StatusCourierAssigned, PolicyAny, nil, []string{"at_pickup", "pickup_confirmed"}},
		{"at_pickup store policy", ModelDirect, StatusAtPickup, PolicyStore, nil, []string{}},
		{"at_pickup courier_and_store, courier confirmed", ModelEndToEnd, StatusAtPickup, PolicyCourierAndStore, courier, []string{}},
		{"at_pickup courier_and_store, only store confirmed", ModelEndToEnd, StatusAtPickup, PolicyCourierAndStore, store, []string{"pickup_confirmed"}},
		{"picked_up", ModelDirect, StatusPickedUp, PolicyAny, nil, []string{"delivered", "failed"}},
		{"arriving", ModelEndToEnd, StatusArriving, PolicyAny, nil, []string{"delivered", "failed"}},
		{"delivered", ModelDirect, StatusDelivered, PolicyAny, nil, []string{}},
		{"failed", ModelDirect, StatusFailed, PolicyAny, nil, []string{}},
		{"cancelled", ModelEndToEnd, StatusCancelled, PolicyAny, nil, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NextActions(tc.model, tc.status, tc.policy, tc.confs); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
