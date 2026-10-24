package postgres

import (
	"encoding/json"
	"fmt"

	"github.com/raduolivas/dt/backend/pkg/sealbox"
)

// Field names in the sealed values' additional data "<delivery id>|<field>".
const (
	fieldDropoffLabel      = "dropoff_label"
	fieldCustomerFirstName = "customer_first_name"
	fieldNotes             = "notes"
	fieldMetadata          = "metadata"
	fieldReason            = "reason"
)

// sealer seals and opens one delivery's personal data, binding each value
// to the delivery and the field so it cannot be moved elsewhere.
type sealer struct {
	box        *sealbox.Box
	deliveryID string
}

func (s sealer) aad(field string) []byte { return []byte(s.deliveryID + "|" + field) }

// sealString seals v; nil stays NULL.
func (s sealer) sealString(field string, v *string) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return s.seal(field, []byte(*v))
}

// openString opens a value sealed by sealString; NULL is nil.
func (s sealer) openString(field string, b []byte) (*string, error) {
	if b == nil {
		return nil, nil
	}
	pt, err := s.open(field, b)
	if err != nil {
		return nil, err
	}
	v := string(pt)
	return &v, nil
}

// sealMap seals m as JSON; a nil map stays NULL and an empty one is "{}".
func (s sealer) sealMap(field string, m map[string]string) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("seal %s: %w", field, err)
	}
	return s.seal(field, b)
}

func (s sealer) openMap(field string, b []byte) (map[string]string, error) {
	if b == nil {
		return nil, nil
	}
	pt, err := s.open(field, b)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(pt, &m); err != nil {
		return nil, fmt.Errorf("delivery %s: %s: %w", s.deliveryID, field, err)
	}
	return m, nil
}

func (s sealer) seal(field string, pt []byte) ([]byte, error) {
	b, err := s.box.Seal(pt, s.aad(field))
	if err != nil {
		return nil, fmt.Errorf("seal %s: %w", field, err)
	}
	return b, nil
}

func (s sealer) open(field string, b []byte) ([]byte, error) {
	pt, err := s.box.Open(b, s.aad(field))
	if err != nil {
		return nil, fmt.Errorf("delivery %s: open %s: %w", s.deliveryID, field, err)
	}
	return pt, nil
}

// reasonField is the field of a history row's reason: the sequence number
// binds each reason to its row.
func reasonField(seq int32) string { return fmt.Sprintf("%s|%d", fieldReason, seq) }

// sealedFields are a delivery's four sealed columns.
type sealedFields struct {
	dropoffLabel, customerFirstName, notes, metadata []byte
}
