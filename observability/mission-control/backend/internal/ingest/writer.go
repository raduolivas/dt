package ingest

import (
	"context"
	"sync"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// DefaultBuffer is the insert buffer size for OTLP and polled events.
const DefaultBuffer = 10000

const (
	writeBatch   = 500
	writeEvery   = 200 * time.Millisecond
	writeTimeout = 5 * time.Second
)

type item struct {
	source string
	ev     store.Event
}

// Writer buffers events and inserts them in batches. Add never blocks: when the
// buffer is full, or the writer has stopped, the event is dropped and counted
// in self-health.
type Writer struct {
	st   *store.Store
	self *Self
	ch   chan item

	// OnInserted, when set before Run, is called per event after a successful
	// insert (order index hook). Its errors are counted, never drop events.
	OnInserted func(ctx context.Context, e store.Event) error

	warn   rateLog
	mu     sync.RWMutex // guards closed against Add
	closed bool
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
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, e := range evs {
		if w.closed {
			w.self.Drop(source, 1)
			continue
		}
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

// Run inserts queued events until ctx ends. Then it refuses new events
// (counted as drops), inserts everything already accepted and returns. Give it
// a context that ends after the producers (HTTP servers, pollers) have stopped.
func (w *Writer) Run(ctx context.Context) {
	t := time.NewTicker(writeEvery)
	defer t.Stop()
	var batch []item
	// Inserts use their own deadline so a cancelled ctx cannot fail the final flush.
	flush := func() {
		if len(batch) == 0 {
			return
		}
		evs := make([]store.Event, len(batch))
		for i, it := range batch {
			evs[i] = it.ev
		}
		fc, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		err := w.st.InsertEvents(fc, evs)
		if err == nil && w.OnInserted != nil {
			for _, it := range batch {
				if ierr := w.OnInserted(fc, it.ev); ierr != nil {
					w.self.StorageError(it.source)
					w.warn.log(w.self.log, "order index update failed", "source", it.source, "error", ierr.Error())
				}
			}
		}
		cancel()
		if err != nil {
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
			w.mu.Lock()
			w.closed = true
			w.mu.Unlock()
			for {
				select {
				case it := <-w.ch:
					batch = append(batch, it)
					if len(batch) >= writeBatch {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		case it := <-w.ch:
			batch = append(batch, it)
			if len(batch) >= writeBatch {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}
