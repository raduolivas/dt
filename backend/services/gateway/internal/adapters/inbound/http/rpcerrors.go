package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
)

// rpcFailure is a failed deliveries call together with the dt-code trailer the
// service set, which lives on the call info, not on the error.
type rpcFailure struct {
	err  error
	code string
}

func (e *rpcFailure) Error() string { return e.err.Error() }
func (e *rpcFailure) Unwrap() error { return e.err }

// callRPC runs fn with a fresh call context and, when it fails, attaches the
// dt-code trailer to the error so toProblem can map it.
func callRPC[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	ctx, info := connect.NewClientContext(ctx)
	res, err := fn(ctx)
	if err != nil {
		return res, &rpcFailure{err: err, code: rpcauth.CodeFrom(info)}
	}
	return res, nil
}

// dtCodeProblems maps the service's dt-code values to public problems.
var dtCodeProblems = map[string]int{
	"not_found":            http.StatusNotFound,
	"validation_failed":    http.StatusBadRequest,
	"invalid_transition":   http.StatusConflict,
	"external_id_conflict": http.StatusConflict,
	"dropoff_locked":       http.StatusConflict,
	"delivery_active":      http.StatusConflict,
	"model_not_allowed":    http.StatusUnprocessableEntity,
	"courier_not_found":    http.StatusUnprocessableEntity,
	// The tracking service's code for "try again shortly".
	"tracking_unavailable": http.StatusServiceUnavailable,
}

// toProblem maps a failed deliveries call to the problem a partner sees. The
// dt-code decides; without one, the Connect code does: NotFound is 404, an
// unreachable or slow service (Unavailable, DeadlineExceeded, Unknown or any
// non-Connect transport error) is 503 service_unavailable, and everything
// else, including a rejected internal token, is our fault: 500. Service error
// messages are never echoed.
func toProblem(err error) httpx.Problem {
	var f *rpcFailure
	code := ""
	if errors.As(err, &f) {
		code = f.code
	}
	if status, ok := dtCodeProblems[code]; ok {
		p := httpx.Problem{Status: status, Code: code}
		switch code {
		case "validation_failed":
			p.Detail = "The request does not match the API contract."
			p.Errors = fieldViolations(err)
		case "tracking_unavailable":
			p.Detail = "Tracking is temporarily unavailable. Retry shortly."
		}
		return p
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return unavailableProblem()
	}
	switch ce.Code() {
	case connect.CodeNotFound:
		return httpx.Problem{Status: http.StatusNotFound, Code: "not_found"}
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeUnknown, connect.CodeCanceled:
		return unavailableProblem()
	default:
		return httpx.Problem{Status: http.StatusInternalServerError, Code: "internal_error"}
	}
}

func unavailableProblem() httpx.Problem {
	return httpx.Problem{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
		Detail: "The service is temporarily unavailable. Retry shortly."}
}

// fieldViolations reads the FieldViolation details of a validation failure.
func fieldViolations(err error) []httpx.FieldError {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return nil
	}
	var out []httpx.FieldError
	for _, d := range ce.Details() {
		m, derr := connectproto.UnmarshalErrorDetail(d)
		if derr != nil {
			continue
		}
		if fv, ok := m.(*deliveriesv1.FieldViolation); ok {
			out = append(out, httpx.FieldError{Field: fv.GetField(), Code: fv.GetCode(), Message: fv.GetMessage()})
		}
	}
	return out
}

// writeRPCError answers a failed deliveries call. 503s and 500s are logged
// (the cause, never request data).
func writeRPCError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	p := toProblem(err)
	switch {
	case p.Status == http.StatusServiceUnavailable:
		log.WarnContext(r.Context(), "deliveries_rpc_unavailable",
			"error", err.Error(), "path", r.URL.Path, "request_id", httpx.RequestIDFrom(r.Context()))
	case p.Status >= http.StatusInternalServerError:
		log.ErrorContext(r.Context(), "deliveries_rpc_failed",
			"error", err.Error(), "path", r.URL.Path, "request_id", httpx.RequestIDFrom(r.Context()))
	}
	httpx.WriteProblem(w, r, p)
}
