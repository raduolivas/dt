package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/raduolivas/dt/backend/pkg/httpx"
)

// apiPrefix is the servers URL path of the contract: its paths are relative
// to it ("/me" is served at "/v1/me").
const apiPrefix = "/v1"

// LoadSpec parses and validates an OpenAPI document.
func LoadSpec(data []byte) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(data)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, err
	}
	return doc, nil
}

// Validate checks each request against spec (paths relative to /v1) and
// answers 400 validation_failed with one errors[] entry per invalid field.
// Security requirements are not checked here (Authenticate and Authorize
// did); the servers' hosts are ignored. A mounted route the spec does not
// describe fails closed with 500, because it means the contract and the
// route table drifted. HEAD is validated as GET. Validate modifies spec (it
// drops its servers).
func Validate(spec *openapi3.T, log *slog.Logger) func(http.Handler) http.Handler {
	validator := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		DoNotValidateServers: true,
		Prefix:               apiPrefix,
		Options: openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
			MultiError:         true,
		},
		ErrorHandlerWithOpts: func(ctx context.Context, err error, w http.ResponseWriter, r *http.Request, opts nethttpmiddleware.ErrorHandlerOpts) {
			if opts.MatchedRoute == nil || opts.StatusCode >= http.StatusInternalServerError {
				log.ErrorContext(ctx, "route_not_in_contract",
					"method", r.Method, "path", r.URL.Path,
					"error", err.Error(),
					"request_id", httpx.RequestIDFrom(ctx))
				internalError(w, r)
				return
			}
			httpx.WriteProblem(w, r, httpx.Problem{
				Status: http.StatusBadRequest, Code: "validation_failed",
				Detail: "The request does not match the API contract.",
				Errors: fieldErrors(err),
			})
		},
	})
	return func(next http.Handler) http.Handler {
		v := validator(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The mux serves HEAD with the GET route; the contract describes
			// only GET, so validate (and serve) it as one. The server still
			// sends no body for HEAD.
			if r.Method == http.MethodHead {
				r = r.Clone(r.Context())
				r.Method = http.MethodGet
			}
			v.ServeHTTP(w, r)
		})
	}
}

// fieldErrors flattens a validation error into one entry per failing field.
// Messages are generic so request values are never echoed back.
func fieldErrors(err error) []httpx.FieldError {
	var out []httpx.FieldError
	collect(err, &out)
	if len(out) == 0 {
		out = append(out, httpx.FieldError{Field: "request", Code: "invalid", Message: "is invalid"})
	}
	return out
}

// collect walks the error tree kin-openapi builds. It switches on concrete
// types on purpose: errors.As would see through a wrapper (RequestError,
// a SchemaError's Origin) and lose the level that names the field.
func collect(err error, out *[]httpx.FieldError) {
	switch e := err.(type) { //nolint:errorlint // structural walk, see above
	case openapi3.MultiError:
		for _, sub := range e {
			collect(sub, out)
		}
	case *openapi3filter.RequestError:
		var se *openapi3.SchemaError
		switch {
		case e.Parameter != nil:
			code := "invalid"
			if errors.As(e.Err, &se) && se.SchemaField != "" {
				code = se.SchemaField
			}
			*out = append(*out, httpx.FieldError{Field: e.Parameter.Name, Code: code, Message: message(code)})
		case errors.As(e.Err, &se):
			collect(e.Err, out)
		default: // not decodable: bad JSON, unsupported content type
			*out = append(*out, httpx.FieldError{Field: "body", Code: "invalid", Message: "is not a valid request body"})
		}
	case *openapi3.SchemaError:
		// A wrapper (allOf, oneOf, a JSON Schema 2020-12 group) carries its causes.
		var nested openapi3.MultiError
		if e.Origin != nil && errors.As(e.Origin, &nested) {
			collect(nested, out)
			return
		}
		*out = append(*out, schemaFieldErrors(e)...)
	default:
		*out = append(*out, httpx.FieldError{Field: "request", Code: "invalid", Message: "is invalid"})
	}
}

var missingProperty = regexp.MustCompile(`property "([^"]+)" is missing|missing propert(?:y|ies) ['"]?([^'",]+)`)

// schemaFieldErrors maps one schema error to field errors. A "required"
// failure points at the parent object, so the missing property names (from
// the reason, which carries only names) are appended to it.
func schemaFieldErrors(se *openapi3.SchemaError) []httpx.FieldError {
	path := strings.Join(se.JSONPointer(), ".")
	code := se.SchemaField
	if code == "" {
		code = "invalid"
	}
	if code == "required" {
		var fes []httpx.FieldError
		for _, m := range missingProperty.FindAllStringSubmatch(se.Reason, -1) {
			name := m[1]
			if name == "" {
				name = m[2]
			}
			fes = append(fes, httpx.FieldError{Field: join(path, name), Code: code, Message: "is required"})
		}
		if len(fes) > 0 {
			return fes
		}
	}
	if path == "" {
		path = "body"
	}
	return []httpx.FieldError{{Field: path, Code: code, Message: message(code)}}
}

func join(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func message(code string) string {
	switch code {
	case "required":
		return "is required"
	case "type":
		return "has the wrong type"
	case "enum", "const":
		return "is not an allowed value"
	case "minLength", "maxLength":
		return "has the wrong length"
	case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
		return "is out of range"
	case "minItems", "maxItems", "uniqueItems":
		return "has the wrong number of items"
	case "pattern", "format":
		return "has an invalid format"
	case "additionalProperties":
		return "is not allowed"
	default:
		return "is invalid"
	}
}
