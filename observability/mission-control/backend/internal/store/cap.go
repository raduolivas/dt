package store

import "unicode/utf8"

// Column caps (M3). The id columns are btree-indexed: one multi-KB value
// exceeds the index row limit and fails the whole insert batch (an OTLP batch
// lost, or a NATS batch nak'd and redelivered forever). DT's ids are far
// shorter than MaxIDLen.
const (
	MaxIDLen     = 128
	MaxReasonLen = 1024
)

// CapEvent enforces the column caps on e in place and returns how many id
// fields it dropped. An oversize id becomes "" (never truncated: a prefix
// could match another order's id); reason is truncated on a rune boundary.
func CapEvent(e *Event) int {
	n := 0
	for _, f := range []*string{&e.Source, &e.Target, &e.TenantID, &e.Env, &e.DeliveryID, &e.OrderRef, &e.TraceID, &e.SpanID} {
		if len(*f) > MaxIDLen {
			*f = ""
			n++
		}
	}
	if len(e.Reason) > MaxReasonLen {
		r := e.Reason[:MaxReasonLen]
		for len(r) > 0 && !utf8.ValidString(r) {
			r = r[:len(r)-1]
		}
		e.Reason = r
	}
	return n
}

// SetOnCapped registers f to be told how many id fields InsertEvents dropped
// (Mission Control counts them as redacted). Call it before the first insert.
func (s *Store) SetOnCapped(f func(n int)) { s.onCapped = f }
