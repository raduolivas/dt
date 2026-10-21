package rpc

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

// errUnauthenticated never says whether the token was missing or wrong.
func errUnauthenticated() error {
	return connect.NewError(connect.CodeUnauthenticated, "unauthenticated")
}

// NewAuthInterceptor rejects every RPC whose Authorization header is not
// "Bearer <token>" (scheme case-insensitive, surrounding whitespace ignored).
// The comparison is constant-time over SHA-256 digests, so neither the
// content nor the length of the token leaks through timing.
func NewAuthInterceptor(token string) (connect.ServerInterceptor, error) {
	if len(token) < MinTokenLen {
		return nil, errors.New("rpc: internal token must be at least 32 bytes")
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

func bearerToken(h string) (string, bool) {
	scheme, tok, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}
