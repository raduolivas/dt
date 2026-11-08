package ingest

import (
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	logsvc "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	tracesvc "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/normalize"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// MaxOTLPBody caps the decoded request body (ruling R11).
const MaxOTLPBody = 4 << 20

// SourceOTLP names the OTLP receiver in self-health.
const SourceOTLP = "otlp"

// OTLPHandler serves POST /v1/traces and POST /v1/logs. It accepts protobuf
// (or JSON) bodies, identity or gzip encoded, normalizes them and queues the
// events on w. It always answers 200 so the collector never retries: bodies it
// cannot use and events that do not fit the buffer are counted as drops.
func OTLPHandler(w *Writer, n *normalize.Normalizer, self *Self, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	h := &otlp{w: w, n: n, self: self, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", h.traces)
	mux.HandleFunc("POST /v1/logs", h.logs)
	return mux
}

type otlp struct {
	w    *Writer
	n    *normalize.Normalizer
	self *Self
	log  *slog.Logger
	warn rateLog
}

func (h *otlp) traces(rw http.ResponseWriter, r *http.Request) {
	var req tracesvc.ExportTraceServiceRequest
	if !h.decode(rw, r, &req) {
		return
	}
	var evs []store.Event
	for _, rs := range req.GetResourceSpans() {
		res := resourceMap(rs.GetResource())
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				evs = append(evs, h.n.FromSpan(res, sp)...)
			}
		}
	}
	h.queue(evs)
	h.reply(rw, r, &tracesvc.ExportTraceServiceResponse{})
}

func (h *otlp) logs(rw http.ResponseWriter, r *http.Request) {
	var req logsvc.ExportLogsServiceRequest
	if !h.decode(rw, r, &req) {
		return
	}
	var evs []store.Event
	for _, rl := range req.GetResourceLogs() {
		res := resourceMap(rl.GetResource())
		for _, sl := range rl.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				evs = append(evs, h.n.FromLog(res, rec))
			}
		}
	}
	h.queue(evs)
	h.reply(rw, r, &logsvc.ExportLogsServiceResponse{})
}

func (h *otlp) queue(evs []store.Event) {
	if len(evs) == 0 {
		return
	}
	var latest time.Time
	for _, e := range evs {
		if e.OccurredAt.After(latest) {
			latest = e.OccurredAt
		}
	}
	if !latest.IsZero() {
		h.self.Lag(SourceOTLP, time.Since(latest))
	}
	h.w.Add(SourceOTLP, evs...)
}

// decode reads the body into msg. On failure it counts a drop, answers 200 and
// returns false.
func (h *otlp) decode(rw http.ResponseWriter, r *http.Request, msg proto.Message) bool {
	body, err := readBody(rw, r)
	if err == nil {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, msg)
		} else {
			err = proto.Unmarshal(body, msg)
		}
	}
	if err != nil {
		h.self.Drop(SourceOTLP, 1)
		h.warn.log(h.log, "otlp body rejected", "error", err.Error())
		h.reply(rw, r, nil)
		return false
	}
	return true
}

func readBody(rw http.ResponseWriter, r *http.Request) ([]byte, error) {
	var src io.Reader = http.MaxBytesReader(rw, r.Body, MaxOTLPBody)
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(src)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		src = gz
	default:
		return nil, errors.New("unsupported content-encoding")
	}
	b, err := io.ReadAll(io.LimitReader(src, MaxOTLPBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxOTLPBody {
		return nil, errors.New("body too large")
	}
	return b, nil
}

func (h *otlp) reply(rw http.ResponseWriter, r *http.Request, resp proto.Message) {
	if resp == nil {
		// Unusable request: an empty 200 still tells the collector not to retry.
		rw.WriteHeader(http.StatusOK)
		return
	}
	var (
		b   []byte
		ct  = "application/x-protobuf"
		err error
	)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		ct = "application/json"
		b, err = protojson.Marshal(resp)
	} else {
		b, err = proto.Marshal(resp)
	}
	if err != nil {
		rw.WriteHeader(http.StatusOK)
		return
	}
	rw.Header().Set("Content-Type", ct)
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(b)
}

func resourceMap(r *resourcepb.Resource) map[string]string {
	out := map[string]string{}
	for _, kv := range r.GetAttributes() {
		if s, ok := kv.GetValue().GetValue().(*commonpb.AnyValue_StringValue); ok {
			out[kv.GetKey()] = s.StringValue
		}
	}
	return out
}
