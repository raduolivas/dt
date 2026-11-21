package outbox

import "go.opentelemetry.io/otel/metric"

// SetMeterForTest makes this package's metrics use m. Call it from a
// non-parallel test and restore with the returned func.
func SetMeterForTest(m metric.Meter) (restore func()) {
	prev := newMeter
	newMeter = func() metric.Meter { return m }
	return func() { newMeter = prev }
}
