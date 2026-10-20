// Package apikey defines the partner secret key format
// dt_sk_<env>_<lookup><secret> and its peppered HMAC hash (spec §11.1).
package apikey

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Environment separates test from live keys.
type Environment string

const (
	Test Environment = "test"
	Live Environment = "live"
)

const (
	secretKeyPrefix = "dt_sk_"
	lookupLen       = 12
	secretLen       = 43
	base62          = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// 248 = 4*62: bytes at or above it are discarded to avoid modulo bias.
	maxUnbiased = 248
	minPepper   = 32
)

// ErrMalformed means the string is not a well formed secret key.
var ErrMalformed = errors.New("apikey: malformed key")

var keyFormat = regexp.MustCompile(`^dt_sk_(test|live)_([0-9A-Za-z]{12})[0-9A-Za-z]{43}$`)

// Key is a freshly generated key. Plaintext is shown to the partner once and
// never stored; Lookup indexes the stored hash.
type Key struct {
	Plaintext     string
	Lookup        string
	DisplayPrefix string
	Env           Environment
}

// Generate creates a key for env from crypto/rand.
func Generate(env Environment) (Key, error) {
	if env != Test && env != Live {
		return Key{}, fmt.Errorf("apikey: unknown environment %q", env)
	}
	chars, err := randomBase62(lookupLen + secretLen)
	if err != nil {
		return Key{}, err
	}
	lookup := chars[:lookupLen]
	return Key{
		Plaintext:     secretKeyPrefix + string(env) + "_" + chars,
		Lookup:        lookup,
		DisplayPrefix: secretKeyPrefix + string(env) + "_" + lookup,
		Env:           env,
	}, nil
}

func randomBase62(n int) (string, error) {
	var sb strings.Builder
	buf := make([]byte, n)
	for sb.Len() < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("apikey: read random: %w", err)
		}
		for _, b := range buf {
			if b >= maxUnbiased {
				continue
			}
			sb.WriteByte(base62[b%62])
			if sb.Len() == n {
				break
			}
		}
	}
	return sb.String(), nil
}

// Parse validates s and returns its environment and lookup.
func Parse(s string) (env Environment, lookup string, err error) {
	m := keyFormat.FindStringSubmatch(s)
	if m == nil {
		return "", "", ErrMalformed
	}
	return Environment(m[1]), m[2], nil
}

// LooksLikeSecretKey reports whether s starts with the secret key prefix. It
// is a cheap check for the browser guard, not a validation.
func LooksLikeSecretKey(s string) bool {
	return strings.HasPrefix(s, secretKeyPrefix)
}

// Hash returns HMAC-SHA256(pepper, plaintext).
func Hash(pepper []byte, plaintext string) []byte {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(plaintext))
	return m.Sum(nil)
}

// Verify compares the hash of plaintext with hash in constant time.
func Verify(pepper []byte, plaintext string, hash []byte) bool {
	return hmac.Equal(Hash(pepper, plaintext), hash)
}

// LoadPepper decodes a base64 pepper and requires at least 32 bytes.
func LoadPepper(b64 string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("apikey: pepper is not valid base64: %w", err)
	}
	if len(b) < minPepper {
		return nil, fmt.Errorf("apikey: pepper must be at least %d bytes, got %d", minPepper, len(b))
	}
	return b, nil
}
