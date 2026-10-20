package domain

import "errors"

var (
	ErrNotFound        = errors.New("not found")
	ErrForbiddenRole   = errors.New("role not allowed for this action")
	ErrLiveNotEnabled  = errors.New("live mode is not enabled for this tenant")
	ErrKeyRevoked      = errors.New("api key is revoked")
	ErrInvalidScope    = errors.New("invalid scope")
	ErrInvalidArgument = errors.New("invalid argument")
)
