package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

// IdempotencyKeyHeader carries the client's idempotency key on every POST.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotentReplayedHeader marks a response replayed from a stored record.
const IdempotentReplayedHeader = "Idempotent-Replayed"

// replayedHeaders are the response headers stored and replayed. Request-Id
// and RateLimit-* belong to the request that is answered, so the outer
// middleware sets them afresh on every response.
var replayedHeaders = []string{"Content-Type"}

// storeTimeout bounds Complete and Abandon, which run detached from the
// request context so a client hanging up does not leave the record locked.
const storeTimeout = 5 * time.Second

// Idempotency is the Middleware.Idempotency hook (POST routes). Every POST
// needs an Idempotency-Key of 1–255 printable ASCII characters (400
// idempotency_key_required / idempotency_key_invalid). Records are scoped by
// the principal's tenant and environment plus the key, and fingerprinted
// with app.IdempotencyRequestHash:
//
//   - first use: the handler runs; a response below 500 with a body of at
//     most 64 KiB is stored before it is sent;
//   - same request again: the stored status, body and Content-Type are
//     replayed with Idempotent-Replayed: true, without running the handler;
//   - another request with the key: 422 idempotency_mismatch;
//   - the first is still running: 409 idempotency_in_progress.
//
// A 5xx, a panic (re-raised for the outer Recover) or a larger response
// abandons the record so the client can retry. If the store fails the request
// is refused with 500: without a record the handler could run twice.
func Idempotency(store app.IdempotencyStore, clock app.Clock, log *slog.Logger) Hook {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler, _ Route) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			values, present := r.Header[http.CanonicalHeaderKey(IdempotencyKeyHeader)]
			if !present {
				httpx.WriteProblem(w, r, httpx.Problem{
					Status: http.StatusBadRequest, Code: "idempotency_key_required",
					Detail: "Send an Idempotency-Key header with every POST.",
				})
				return
			}
			if len(values) != 1 || !app.ValidIdempotencyKey(values[0]) {
				httpx.WriteProblem(w, r, httpx.Problem{
					Status: http.StatusBadRequest, Code: "idempotency_key_invalid",
					Detail: "Idempotency-Key must be one value of 1 to 255 printable ASCII characters.",
				})
				return
			}
			p, ok := PrincipalFrom(r.Context())
			if !ok { // Authenticate always runs first; fail closed
				log.ErrorContext(r.Context(), "idempotency_without_principal",
					"request_id", httpx.RequestIDFrom(r.Context()))
				internalError(w, r)
				return
			}
			body, err := io.ReadAll(r.Body) // buffered by MaxBytes, ≤ 64 KiB
			if err != nil {
				idempotencyUnavailable(log, w, r, err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			scope := app.IdempotencyScope{TenantID: p.TenantID, Env: p.Env, Key: values[0]}
			hash := app.IdempotencyRequestHash(r.Method, r.URL.Path, body)
			out, err := store.Begin(r.Context(), scope, hash, clock.Now())
			if err != nil {
				idempotencyUnavailable(log, w, r, err)
				return
			}
			switch out.Kind {
			case app.OutcomeStarted:
				runAndRecord(w, r, next, store, scope, out.Lease, log)
			case app.OutcomeReplay:
				replay(w, out.Stored)
			case app.OutcomeMismatch:
				httpx.WriteProblem(w, r, httpx.Problem{
					Status: http.StatusUnprocessableEntity, Code: "idempotency_mismatch",
					Detail: "This Idempotency-Key was already used with a different request.",
				})
			case app.OutcomeInProgress:
				httpx.WriteProblem(w, r, httpx.Problem{
					Status: http.StatusConflict, Code: "idempotency_in_progress",
					Detail: "A request with this Idempotency-Key is still in progress; retry later.",
				})
			default:
				idempotencyUnavailable(log, w, r, errors.New("unknown outcome "+out.Kind.String()))
			}
		})
	}
}

func idempotencyUnavailable(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	log.ErrorContext(r.Context(), "idempotency_unavailable",
		"error", err.Error(),
		"request_id", httpx.RequestIDFrom(r.Context()))
	internalError(w, r)
}

func replay(w http.ResponseWriter, st app.StoredResponse) {
	h := w.Header()
	for _, name := range replayedHeaders {
		if vs, ok := st.Header[name]; ok {
			h[name] = append([]string(nil), vs...)
		}
	}
	h.Set(IdempotentReplayedHeader, "true")
	w.WriteHeader(st.Status)
	_, _ = w.Write(st.Body)
}

// runAndRecord runs next with its response buffered, stores the response
// under lease and then sends it. A 5xx, an oversized body or a panic abandons
// the record instead.
func runAndRecord(w http.ResponseWriter, r *http.Request, next http.Handler, store app.IdempotencyStore,
	scope app.IdempotencyScope, lease time.Time, log *slog.Logger,
) {
	rec := &recorder{w: w, status: http.StatusOK}
	finished := false
	defer func() {
		if finished {
			return
		}
		// next panicked (http.ErrAbortHandler included): free the key, then
		// let the outer Recover answer.
		p := recover()
		abandon(r, store, scope, lease, log)
		if p != nil { // nil: runtime.Goexit, which keeps unwinding
			panic(p)
		}
	}()
	next.ServeHTTP(rec, r)
	finished = true

	if rec.overflow || rec.status >= http.StatusInternalServerError {
		abandon(r, store, scope, lease, log)
		rec.flush()
		return
	}
	st := app.StoredResponse{Status: rec.status, Body: rec.buf.Bytes()}
	for _, name := range replayedHeaders {
		if vs, ok := w.Header()[name]; ok {
			if st.Header == nil {
				st.Header = map[string][]string{}
			}
			st.Header[name] = append([]string(nil), vs...)
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), storeTimeout)
	defer cancel()
	if err := store.Complete(ctx, scope, lease, st); err != nil {
		// The work is done: answer it. The record stays in progress until
		// its lease passes (409 to duplicates meanwhile) — never a replay of
		// a response that was not stored.
		log.ErrorContext(r.Context(), "idempotency_complete_failed",
			"error", err.Error(),
			"tenant_id", scope.TenantID,
			"request_id", httpx.RequestIDFrom(r.Context()))
	}
	rec.flush()
}

func abandon(r *http.Request, store app.IdempotencyStore, scope app.IdempotencyScope, lease time.Time, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), storeTimeout)
	defer cancel()
	if err := store.Abandon(ctx, scope, lease); err != nil {
		log.ErrorContext(r.Context(), "idempotency_abandon_failed",
			"error", err.Error(),
			"tenant_id", scope.TenantID,
			"request_id", httpx.RequestIDFrom(r.Context()))
	}
}

// recorder buffers a handler's response up to MaxIdempotentResponseBytes.
// Headers go straight to the real writer's map. Past the limit it sends what
// it holds and streams the rest (overflow), and the record is abandoned.
//
// It deliberately has no Unwrap: a handler flushing through
// http.ResponseController would send a response before it is stored.
type recorder struct {
	w        http.ResponseWriter
	status   int
	chosen   bool // the handler picked its final status
	sent     bool // the status went out on w
	buf      bytes.Buffer
	overflow bool
}

func (c *recorder) Header() http.Header { return c.w.Header() }

func (c *recorder) WriteHeader(status int) {
	if c.chosen {
		return
	}
	if status >= 100 && status < 200 { // informational: pass through
		c.w.WriteHeader(status)
		return
	}
	c.chosen = true
	c.status = status
}

func (c *recorder) Write(b []byte) (int, error) {
	if !c.chosen {
		c.WriteHeader(http.StatusOK)
	}
	if c.overflow {
		return c.w.Write(b)
	}
	if c.buf.Len()+len(b) <= app.MaxIdempotentResponseBytes {
		return c.buf.Write(b)
	}
	c.overflow = true
	c.flush()
	return c.w.Write(b)
}

// flush sends the status (once) and the buffered body to the real writer.
func (c *recorder) flush() {
	if !c.sent {
		c.sent = true
		c.w.WriteHeader(c.status)
	}
	if c.buf.Len() > 0 {
		_, _ = c.w.Write(c.buf.Bytes())
		c.buf.Reset()
	}
}
