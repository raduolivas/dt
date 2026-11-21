package app

import "go.opentelemetry.io/otel/metric"

// setMeterForTest makes this package's metrics use m; restore with the
// returned func. Callers must not be parallel.
func setMeterForTest(m metric.Meter) (restore func()) {
	metricsMu.Lock()
	prev := newMeter
	newMeter = func() metric.Meter { return m }
	fixesCtr = nil
	metricsMu.Unlock()
	return func() {
		metricsMu.Lock()
		newMeter, fixesCtr = prev, nil
		metricsMu.Unlock()
	}
}
