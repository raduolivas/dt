// Package rpcauth is the shared-secret auth of the internal Connect RPC APIs
// (gateway to accounts and deliveries): a bearer-token server interceptor, the
// matching client interceptor, and the dt-code error trailer.
package rpcauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"

	"connectrpc.com/connect/v2"
)

// MinTokenLen is the minimum INTERNAL_RPC_TOKEN length in bytes.
const MinTokenLen = 32

// CodeKey is the response trailer carrying the snake_case error code. Over
// the Connect protocol it arrives as the HTTP header "Trailer-Dt-Code";
// clients read it with connect.NewClientContext and CodeFrom.
const CodeKey = "dt-code"

var errShortToken = errors.New("rpcauth: internal token must be at least 32 bytes")

// errUnauthenticated never says whether the token was missing or wrong.
func errUnauthenticated() error {
	return connect.NewError(connect.CodeUnauthenticated, "unauthenticated")
}

// NewServerInterceptor rejects every RPC whose Authorization header is not
// "Bearer <token>" (scheme case-insensitive, surrounding whitespace ignored).
// The comparison is constant-time over SHA-256 digests, so neither the
// content nor the length of the token leaks through timing.
func NewServerInterceptor(token string) (connect.ServerInterceptor, error) {
	if len(token) < MinTokenLen {
		return nil, errShortToken
	}
	want := sha256.Sum256([]byte(token))
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			info, ok := connect.CallInfoForServerContext(ctx)
			if !ok {
				return errUnauthenticated()
			}
			got, ok := bearerToken(info.RequestHeader().Get("Authorization"))
			if !ok {
				return errUnauthenticated()
			}
			sum := sha256.Sum256([]byte(got))
			if subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
				return errUnauthenticated()
			}
			return next(ctx, spec, stream)
		}
	}, nil
}

// NewClientInterceptor sends "Authorization: Bearer <token>" on every RPC.
func NewClientInterceptor(token string) (connect.ClientInterceptor, error) {
	if len(token) < MinTokenLen {
		return nil, errShortToken
	}
	value := "Bearer " + token
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			if info, ok := connect.CallInfoForClientContext(ctx); ok {
				info.RequestHeader().Set("Authorization", value)
			}
			return next(ctx, spec)
		}
	}, nil
}

// SetCode sets the dt-code response trailer of the RPC being served.
func SetCode(ctx context.Context, code string) {
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		info.ResponseTrailer().Set(CodeKey, code)
	}
}

// CodeFrom returns the dt-code trailer of a finished client call, or "".
func CodeFrom(info *connect.CallInfo) string {
	return info.ResponseTrailer().Get(CodeKey)
}

func bearerToken(h string) (string, bool) {
	scheme, tok, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}
