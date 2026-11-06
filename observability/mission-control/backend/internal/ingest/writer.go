package ingest

import (
	"context"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// DefaultBuffer is the insert buffer size for OTLP and polled events.
const DefaultBuffer = 10000

const (
	writeBatch = 500
	writeEvery = 200 * time.Millisecond
)

type item struct {
	source string
	ev     store.Event
}

// Writer buffers events and inserts them in batches. Add never blocks: when the
// buffer is full the event is dropped and counted in self-health.
type Writer struct {
	st   *store.Store
	self *Self
	ch   chan item
}

// NewWriter returns a Writer with room for size events.
func NewWriter(st *store.Store, self *Self, size int) *Writer {
	if size < 1 {
		size = 1
	}
	return &Writer{st: st, self: self, ch: make(chan item, size)}
}

// Add queues events from source, dropping (and counting) what does not fit.
func (w *Writer) Add(source string, evs ...store.Event) {
	for _, e := range evs {
		select {
		case w.ch <- item{source, e}:
		default:
			w.self.Drop(source, 1)
		}
	}
}

// Len is the number of events waiting to be inserted.
func (w *Writer) Len() int { return len(w.ch) }

// Sink adapts Add for the pollers.
func (w *Writer) Sink(source string) func(store.Event) {
	return func(e store.Event) { w.Add(source, e) }
}

// Run inserts queued events until ctx ends, then flushes what is left.
func (w *Writer) Run(ctx context.Context) {
	t := time.NewTicker(writeEvery)
	defer t.Stop()
	var batch []item
	flush := func(c context.Context) {
		if len(batch) == 0 {
			return
		}
		evs := make([]store.Event, len(batch))
		for i, it := range batch {
			evs[i] = it.ev
		}
		if err := w.st.InsertEvents(c, evs); err != nil {
			seen := map[string]bool{}
			for _, it := range batch {
				if !seen[it.source] {
					seen[it.source] = true
					w.self.StorageError(it.source)
				}
			}
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case it := <-w.ch:
					batch = append(batch, it)
					continue
				default:
				}
				break
			}
			fc, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			flush(fc)
			cancel()
			return
		case it := <-w.ch:
			batch = append(batch, it)
			if len(batch) >= writeBatch {
				flush(ctx)
			}
		case <-t.C:
			flush(ctx)
		}
	}
}
