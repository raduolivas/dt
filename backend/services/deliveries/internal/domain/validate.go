package domain

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Input limits (global constraints); the OpenAPI schema states them too, but
// the domain re-checks every one.
const (
	maxIDLen           = 255
	maxPickupNameLen   = 100
	maxDropoffLabelLen = 100
	maxFirstNameLen    = 50
	maxItemsSummaryLen = 200
	maxItemCount       = 999
	maxNotesLen        = 500
	maxMetadataKeys    = 50
	maxMetadataKeyLen  = 40
	maxMetadataValLen  = 500
	maxReasonLen       = 200
	maxDisplayNameLen  = 50
	maxPhotoURLLen     = 2048
)

// checkASCIIID checks a 1–255 printable-ASCII identifier.
func checkASCIIID(field, v string) error {
	if v == "" {
		return invalid(field, "required", "must not be empty")
	}
	if len(v) > maxIDLen {
		return invalid(field, "too_long", fmt.Sprintf("must be at most %d characters", maxIDLen))
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return invalid(field, "invalid", "must be printable ASCII")
		}
	}
	return nil
}

// checkLen checks v is at most max characters.
func checkLen(field, v string, max int) error {
	if utf8.RuneCountInString(v) > max {
		return invalid(field, "too_long", fmt.Sprintf("must be at most %d characters", max))
	}
	return nil
}

func checkOptLen(field string, v *string, max int) error {
	if v == nil {
		return nil
	}
	return checkLen(field, *v, max)
}

func checkLocation(field string, l Location) error {
	if !l.valid() {
		return invalid(field, "out_of_range", "lat must be within ±90 and lng within ±180")
	}
	return nil
}

func checkDistance(pickup, dropoff Location) error {
	if distanceKm(pickup, dropoff) > MaxPickupDropoffKm {
		return invalid("dropoff.location", "too_far", fmt.Sprintf("must be at most %d km from the pickup", MaxPickupDropoffKm))
	}
	return nil
}

func checkMetadata(m map[string]string) error {
	if len(m) > maxMetadataKeys {
		return invalid("metadata", "too_many", fmt.Sprintf("must have at most %d keys", maxMetadataKeys))
	}
	for k, v := range m {
		if !validMetadataKey(k) {
			return invalid("metadata", "invalid_key", fmt.Sprintf("keys must be 1 to %d characters of a-zA-Z0-9_", maxMetadataKeyLen))
		}
		if utf8.RuneCountInString(v) > maxMetadataValLen {
			return invalid("metadata", "too_long", fmt.Sprintf("values must be at most %d characters", maxMetadataValLen))
		}
	}
	return nil
}

func validMetadataKey(k string) bool {
	if k == "" || len(k) > maxMetadataKeyLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		if !isKeyChar(k[i]) {
			return false
		}
	}
	return true
}

func isKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func checkPhotoURL(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > maxPhotoURLLen {
		return invalid("photo_url", "too_long", fmt.Sprintf("must be at most %d characters", maxPhotoURLLen))
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || strings.ContainsAny(v, " \t\r\n") {
		return invalid("photo_url", "invalid", "must be an https URL")
	}
	return nil
}
