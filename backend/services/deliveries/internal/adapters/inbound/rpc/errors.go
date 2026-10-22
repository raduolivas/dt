package rpc

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

var errorCodes = []struct {
	err    error
	code   connect.Code
	dtCode string
}{
	{app.ErrNotFound, connect.CodeNotFound, "not_found"},
	{domain.ErrInvalidTransition, connect.CodeFailedPrecondition, "invalid_transition"},
	{app.ErrExternalIDTaken, connect.CodeAlreadyExists, "external_id_conflict"},
	{domain.ErrDropoffLocked, connect.CodeFailedPrecondition, "dropoff_locked"},
	{domain.ErrDeliveryActive, connect.CodeFailedPrecondition, "delivery_active"},
	{domain.ErrModelNotAllowed, connect.CodeInvalidArgument, "model_not_allowed"},
	{app.ErrCourierNotFound, connect.CodeInvalidArgument, "courier_not_found"},
}

// ToConnect maps a use-case error to a Connect error and sets the dt-code
// response trailer. Validation errors carry a FieldViolation detail. Domain
// errors keep their message, which holds no personal data; anything else is
// logged and becomes CodeInternal with no detail.
func ToConnect(ctx context.Context, log *slog.Logger, err error) error {
	if err == nil {
		return nil
	}
	if cerr, ok := errors.AsType[*connect.Error](err); ok {
		return cerr
	}
	if verr, ok := errors.AsType[*domain.ValidationError](err); ok {
		rpcauth.SetCode(ctx, "validation_failed")
		cerr := connect.NewError(connect.CodeInvalidArgument, verr.Error()).WithCause(err)
		detail, derr := connectproto.NewErrorDetail(&deliveriesv1.FieldViolation{
			Field: verr.Field, Code: verr.Code, Message: verr.Message,
		})
		if derr == nil {
			cerr = cerr.WithDetail(detail)
		}
		return cerr
	}
	for _, m := range errorCodes {
		if errors.Is(err, m.err) {
			rpcauth.SetCode(ctx, m.dtCode)
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
