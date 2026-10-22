// Package sealbox provides AES-256-GCM field encryption with key ids so keys
// can be rotated while old data stays readable.
//
// Sealed format: 0x01 | len(id) | id | 12-byte nonce | GCM ciphertext+tag.
package sealbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	version   = 0x01
	keyLen    = 32
	nonceSize = 12
)

// ErrOpen covers every failure to open a sealed value: tampering, an unknown
// key id, a wrong AAD or a malformed blob.
var ErrOpen = errors.New("sealbox: cannot open sealed value")

var idPattern = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// Box seals with the current key and opens with any configured key.
type Box struct {
	currentID string
	aeads     map[string]cipher.AEAD
}

// ParseKeys builds a Box from spec (`id:base64[,id:base64...]`) and the id of
// the key used for new seals. Errors never contain key material; keys are
// referenced by position or id only.
func ParseKeys(spec, current string) (*Box, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, errors.New("sealbox: no keys configured")
	}
	if current == "" {
		return nil, errors.New("sealbox: current key id is required")
	}
	b := &Box{currentID: current, aeads: map[string]cipher.AEAD{}}
	for i, entry := range strings.Split(spec, ",") {
		id, enc, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok {
			return nil, fmt.Errorf("sealbox: key #%d: expected id:base64", i+1)
		}
		if !idPattern.MatchString(id) {
			return nil, fmt.Errorf("sealbox: key #%d: id must match %s", i+1, idPattern)
		}
		if _, dup := b.aeads[id]; dup {
			return nil, fmt.Errorf("sealbox: key %q: duplicate id", id)
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("sealbox: key %q: not valid base64", id)
		}
		if len(raw) != keyLen {
			return nil, fmt.Errorf("sealbox: key %q: must decode to %d bytes", id, keyLen)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, fmt.Errorf("sealbox: key %q: invalid", id)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("sealbox: key %q: invalid", id)
		}
		b.aeads[id] = aead
	}
	if _, ok := b.aeads[current]; !ok {
		return nil, fmt.Errorf("sealbox: current key id %q is not in the key set", current)
	}
	return b, nil
}

// Seal encrypts plaintext under the current key, binding aad.
func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	aead := b.aeads[b.currentID]
	out := make([]byte, 0, 2+len(b.currentID)+nonceSize+len(plaintext)+aead.Overhead())
	out = append(out, version, byte(len(b.currentID)))
	out = append(out, b.currentID...)
	nonceStart := len(out)
	out = append(out, make([]byte, nonceSize)...)
	if _, err := rand.Read(out[nonceStart:]); err != nil {
		return nil, fmt.Errorf("sealbox: nonce: %w", err)
	}
	return aead.Seal(out, out[nonceStart:], plaintext, aad), nil
}

// Open decrypts a value produced by Seal using the key named in the blob.
func (b *Box) Open(sealed, aad []byte) ([]byte, error) {
	if len(sealed) < 2 || sealed[0] != version {
		return nil, ErrOpen
	}
	idLen := int(sealed[1])
	if len(sealed) < 2+idLen+nonceSize {
		return nil, ErrOpen
	}
	aead, ok := b.aeads[string(sealed[2:2+idLen])]
	if !ok {
		return nil, ErrOpen
	}
	rest := sealed[2+idLen:]
	if len(rest) < nonceSize+aead.Overhead() {
		return nil, ErrOpen
	}
	pt, err := aead.Open(nil, rest[:nonceSize], rest[nonceSize:], aad)
	if err != nil {
		return nil, ErrOpen
	}
	return pt, nil
}
