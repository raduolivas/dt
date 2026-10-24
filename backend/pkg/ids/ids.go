// Package ids creates prefixed, time-sortable identifiers such as
// ten_01j9... (prefix, underscore, lowercase ULID).
package ids

import (
	"crypto/rand"
	"strings"
	"sync"

	"github.com/oklog/ulid/v2"
)

// Prefixes of the entities that carry an ID.
const (
	Tenant = "ten"
	APIKey = "ak"
	Audit  = "aud"
	Event  = "evt"
	// Delivery and Courier identify the deliveries service's entities.
	Delivery = "del"
	Courier  = "cou"
)

var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// New returns prefix + "_" + a lowercase ULID. IDs are monotonic within a
// millisecond and New is safe for concurrent use.
func New(prefix string) string {
	mu.Lock()
	id, err := ulid.New(ulid.Now(), entropy)
	mu.Unlock()
	if err != nil {
		// Only possible if crypto/rand fails or the monotonic counter
		// overflows; neither can be recovered from here.
		panic("ids: " + err.Error())
	}
	return prefix + "_" + strings.ToLower(id.String())
}

// Valid reports whether s is an ID created with prefix.
func Valid(prefix, s string) bool {
	rest, ok := strings.CutPrefix(s, prefix+"_")
	if !ok || len(rest) != ulid.EncodedSize || rest != strings.ToLower(rest) {
		return false
	}
	_, err := ulid.ParseStrict(rest)
	return err == nil
}
