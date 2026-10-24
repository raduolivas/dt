package domain

import (
	"errors"
	"time"
)

// Domain errors; the RPC layer maps them to problem codes.
var (
	ErrInvalidTransition = errors.New("invalid transition")
	ErrModelNotAllowed   = errors.New("delivery model not allowed for this tenant")
	ErrDropoffLocked     = errors.New("dropoff location cannot change after pickup")
	ErrDeliveryActive    = errors.New("delivery is still active")
)

// Timing and distance rules.
const (
	ConflictWindow     = 60 * time.Second
	PickupPendingAfter = 10 * time.Minute
	MaxFutureSkew      = 5 * time.Minute
	MaxPickupDropoffKm = 100
)

// ValidationError is an input that breaks a limit. Field is the API field
// path (e.g. "dropoff.location"), Code a short machine code.
type ValidationError struct {
	Field, Code, Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func invalid(field, code, msg string) error {
	return &ValidationError{Field: field, Code: code, Message: msg}
}
