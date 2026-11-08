// Package redact is Mission Control's privacy filter for span and log
// attributes. It applies the same deny-list as logx (the segment split rule
// "_", "." and "-"), built from logx.SensitiveKeys() and
// logx.SensitiveSegments() so the two cannot drift, and drops values that look
// like DT API keys.
package redact

import (
	"regexp"
	"strings"

	"github.com/raduolivas/dt/backend/pkg/logx"
)

var (
	segments = toSet(logx.SensitiveSegments())
	wholeKey = func() map[string]struct{} {
		s := toSet(logx.SensitiveKeys())
		for k := range segments {
			delete(s, k)
		}
		return s
	}()
	// Unanchored on purpose: a key quoted inside a longer string is still a key.
	secretValue = regexp.MustCompile(`(dt_sk_|dt_pk_)`)
)

func toSet(in []string) map[string]struct{} {
	m := make(map[string]struct{}, len(in))
	for _, k := range in {
		m[k] = struct{}{}
	}
	return m
}

// SensitiveKey reports whether a key is on the deny-list (same rule as logx).
func SensitiveKey(key string) bool {
	k := strings.ToLower(key)
	if _, ok := wholeKey[k]; ok {
		return true
	}
	for _, seg := range strings.FieldsFunc(k, func(r rune) bool { return r == '_' || r == '.' || r == '-' }) {
		if _, ok := segments[seg]; ok {
			return true
		}
	}
	return false
}

// SecretValue reports whether a string carries a DT API key.
func SecretValue(s string) bool { return secretValue.MatchString(s) }

// Attrs returns a copy of in without sensitive keys or secret-looking values,
// at any depth, and the number of entries dropped. in is not modified.
func Attrs(in map[string]any) (out map[string]any, dropped int) {
	out = make(map[string]any, len(in))
	for k, v := range in {
		if SensitiveKey(k) {
			dropped++
			continue
		}
		nv, keep, d := value(v)
		dropped += d
		if keep {
			out[k] = nv
		}
	}
	return out, dropped
}

func value(v any) (out any, keep bool, dropped int) {
	switch x := v.(type) {
	case string:
		if SecretValue(x) {
			return nil, false, 1
		}
		return x, true, 0
	case map[string]any:
		m, d := Attrs(x)
		return m, true, d
	case []any:
		res := make([]any, 0, len(x))
		for _, e := range x {
			nv, k, d := value(e)
			dropped += d
			if k {
				res = append(res, nv)
			}
		}
		return res, true, dropped
	case []string:
		res := make([]any, 0, len(x))
		for _, e := range x {
			if SecretValue(e) {
				dropped++
				continue
			}
			res = append(res, e)
		}
		return res, true, dropped
	default:
		return v, true, 0
	}
}
