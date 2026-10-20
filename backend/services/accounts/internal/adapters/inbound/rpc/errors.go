package rpc

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect/v2"

	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
)

// DTCodeKey is the response trailer carrying the snake_case error code. Over
// the Connect protocol it arrives as the HTTP header "Trailer-Dt-Code";
// clients read it with connect.NewClientContext and info.ResponseTrailer().
const DTCodeKey = "dt-code"

var errorCodes = []struct {
	err    error
	code   connect.Code
	dtCode string
}{
	{domain.ErrNotFound, connect.CodeNotFound, "not_found"},
	{domain.ErrForbiddenRole, connect.CodePermissionDenied, "forbidden_role"},
	{domain.ErrLiveNotEnabled, connect.CodeFailedPrecondition, "live_not_enabled"},
	{domain.ErrKeyRevoked, connect.CodeFailedPrecondition, "key_revoked"},
	{domain.ErrInvalidScope, connect.CodeInvalidArgument, "invalid_scope"},
	{domain.ErrInvalidArgument, connect.CodeInvalidArgument, "invalid_argument"},
}

// ToConnect maps a use-case error to a Connect error and sets the dt-code
// response trailer (HTTP header Trailer-Dt-Code over the Connect protocol). Domain errors keep their message, which never holds a
// secret; anything else is logged and becomes CodeInternal with no detail.
func ToConnect(ctx context.Context, log *slog.Logger, err error) error {
	if err == nil {
		return nil
	}
	if cerr, ok := errors.AsType[*connect.Error](err); ok {
		return cerr
	}
	for _, m := range errorCodes {
		if errors.Is(err, m.err) {
			if info, ok := connect.CallInfoForServerContext(ctx); ok {
				info.ResponseTrailer().Set(DTCodeKey, m.dtCode)
			}
			return connect.NewError(m.code, err.Error()).WithCause(err)
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, "canceled").WithCause(err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, "deadline exceeded").WithCause(err)
	}
	log.ErrorContext(ctx, "rpc_internal_error", slog.Any("error", err))
	return connect.NewError(connect.CodeInternal, "internal error").WithCause(err)
}
