package domain

import (
	"fmt"
	"testing"
)

// controlChars are rejected in every clear-text field (PostgreSQL text
// refuses NUL, and none of them belong in these fields).
var controlChars = []string{"\x00", "\x01", "\t", "\n", "\r", "\x1f", "\x7f"}

func TestTextFieldsRejectControlCharacters(t *testing.T) {
	for _, c := range controlChars {
		bad := "a" + c + "b"
		name := func(field string) string { return fmt.Sprintf("%s %q", field, c) }

		creates := map[string]func(*NewDeliveryInput){
			"pickup.name":         func(in *NewDeliveryInput) { in.PickupName = bad },
			"dropoff.label":       func(in *NewDeliveryInput) { in.DropoffLabel = ptr(bad) },
			"customer.first_name": func(in *NewDeliveryInput) { in.CustomerFirstName = ptr(bad) },
			"items_summary":       func(in *NewDeliveryInput) { in.ItemsSummary = ptr(bad) },
			"notes":               func(in *NewDeliveryInput) { in.Notes = ptr(bad) },
			"metadata":            func(in *NewDeliveryInput) { in.Metadata = map[string]string{"k": bad} },
		}
		for field, mod := range creates {
			t.Run("create "+name(field), func(t *testing.T) {
				in := validInput()
				mod(&in)
				_, _, err := NewDelivery("del_1", "ten_1", "test", in, DefaultTenantSettings(), t0)
				wantValidation(t, err, field, "invalid_characters")
			})
		}

		patches := map[string]Patch{
			"dropoff.label": {DropoffLabel: ptr(ptr(bad))},
			"notes":         {Notes: ptr(ptr(bad))},
			"metadata":      {Metadata: map[string]string{"k": bad}, MetadataSet: true},
		}
		for field, p := range patches {
			t.Run("update "+name(field), func(t *testing.T) {
				d := newDel(t, ModelDirect, PolicyAny)
				_, err := d.Update(p, t0)
				wantValidation(t, err, field, "invalid_characters")
			})
		}

		t.Run("cancel "+name("reason"), func(t *testing.T) {
			d := newDel(t, ModelDirect, PolicyAny)
			_, err := d.Cancel(bad, t0)
			wantValidation(t, err, "reason", "invalid_characters")
		})
		t.Run("report "+name("reason"), func(t *testing.T) {
			d := newDel(t, ModelDirect, PolicyAny)
			r := rep("e1", ReportFailed, ActorPartner, t0)
			r.Reason = bad
			_, _, err := d.Report(r)
			wantValidation(t, err, "reason", "invalid_characters")
		})
		t.Run("courier "+name("display_name"), func(t *testing.T) {
			_, err := NewCourier("cou_1", CourierInput{ExternalID: "c1", DisplayName: bad, Vehicle: VehicleCar}, t0)
			wantValidation(t, err, "display_name", "invalid_characters")
		})
	}
}

func TestTextFieldsAcceptUnicode(t *testing.T) {
	in := validInput()
	in.PickupName = "Café São João — 🍕"
	in.Notes = ptr("Toque à campainha, 2º andar")
	if _, _, err := NewDelivery("del_1", "ten_1", "test", in, DefaultTenantSettings(), t0); err != nil {
		t.Fatalf("NewDelivery: %v", err)
	}
}
