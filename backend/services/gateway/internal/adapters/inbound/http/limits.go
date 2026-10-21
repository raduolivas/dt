package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/raduolivas/dt/backend/pkg/httpx"
)

func payloadTooLarge(w http.ResponseWriter, r *http.Request, limit int64) {
	httpx.WriteProblem(w, r, httpx.Problem{
		Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large",
		Detail: "The request body must be at most " + strconv.FormatInt(limit, 10) + " bytes.",
	})
}

// MaxBytes refuses a request body larger than limit with 413
// payload_too_large. A declared Content-Length over the limit is refused
// before reading; otherwise the body (chunked or not) is read up to limit+1
// bytes and buffered, so later steps (idempotency hashing, validation, the
// handler) can each read it in full without ever seeing more than limit.
func MaxBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				payloadTooLarge(w, r, limit)
				return
			}
			if r.Body == nil || r.Body == http.NoBody {
				next.ServeHTTP(w, r)
				return
			}
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
			var tooBig *http.MaxBytesError
			switch {
			case errors.As(err, &tooBig):
				payloadTooLarge(w, r, limit)
				return
			case err != nil:
				httpx.WriteProblem(w, r, httpx.Problem{
					Status: http.StatusBadRequest, Code: "validation_failed",
					Detail: "The request body could not be read.",
					Errors: []httpx.FieldError{{Field: "body", Code: "unreadable", Message: "could not be read"}},
				})
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(b))
			r.ContentLength = int64(len(b))
			next.ServeHTTP(w, r)
		})
	}
}
