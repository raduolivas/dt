package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/raduolivas/dt/backend/pkg/httpx"
)

var generatedID = regexp.MustCompile(`^req_[a-z2-7]{26}$`)

func serveWithID(t *testing.T, clientID *string) (header, inCtx string) {
	t.Helper()
	h := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inCtx = httpx.RequestIDFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if clientID != nil {
		req.Header.Set(httpx.RequestIDHeader, *clientID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Header().Get(httpx.RequestIDHeader), inCtx
}

func TestRequestIDGeneratesWhenMissing(t *testing.T) {
	header, inCtx := serveWithID(t, nil)
	if !generatedID.MatchString(header) {
		t.Fatalf("header = %q", header)
	}
	if inCtx != header {
		t.Fatalf("context %q != header %q", inCtx, header)
	}
}

func TestRequestIDKeepsSafeClientValue(t *testing.T) {
	id := "abc-123_X"
	header, inCtx := serveWithID(t, &id)
	if header != id || inCtx != id {
		t.Fatalf("header %q ctx %q", header, inCtx)
	}
}

func TestRequestIDReplacesUnsafeClientValue(t *testing.T) {
	for name, id := range map[string]string{
		"newline":  "evil\nINJECT",
		"too_long": strings.Repeat("a", 65),
		"empty":    "",
	} {
		t.Run(name, func(t *testing.T) {
			header, _ := serveWithID(t, &id)
			if !generatedID.MatchString(header) {
				t.Fatalf("header = %q", header)
			}
		})
	}
}
