package httpapi

import "go.opentelemetry.io/otel/metric"

// SetMeterForTest makes this package's metrics use m. Call it from a
// non-parallel test and restore with the returned func.
func SetMeterForTest(m metric.Meter) (restore func()) {
	metricsMu.Lock()
	prev := newMeter
	newMeter = func() metric.Meter { return m }
	cur, fixToViewer = nil, nil
	metricsMu.Unlock()
	return func() {
		metricsMu.Lock()
		newMeter, cur, fixToViewer = prev, nil, nil
		metricsMu.Unlock()
	}
}
