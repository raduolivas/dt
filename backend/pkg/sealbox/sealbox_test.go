package sealbox_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/backend/pkg/sealbox"
)

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func newBox(t *testing.T) *sealbox.Box {
	t.Helper()
	b, err := sealbox.ParseKeys("k1:"+key(1), "k1")
	require.NoError(t, err)
	return b
}

func TestSealOpenRoundTrip(t *testing.T) {
	b := newBox(t)
	for _, pt := range [][]byte{[]byte("Ana"), {}, bytes.Repeat([]byte("x"), 4096)} {
		sealed, err := b.Seal(pt, []byte("d1|first_name"))
		require.NoError(t, err)
		got, err := b.Open(sealed, []byte("d1|first_name"))
		require.NoError(t, err)
		require.True(t, bytes.Equal(pt, got))
	}
}

func TestOpenWrongAADFails(t *testing.T) {
	b := newBox(t)
	sealed, err := b.Seal([]byte("Ana"), []byte("d1|first_name"))
	require.NoError(t, err)
	_, err = b.Open(sealed, []byte("d2|first_name"))
	require.ErrorIs(t, err, sealbox.ErrOpen)
}

func TestOpenTamperedFails(t *testing.T) {
	b := newBox(t)
	sealed, err := b.Seal([]byte("Ana"), []byte("aad"))
	require.NoError(t, err)
	for i := range sealed {
		c := append([]byte(nil), sealed...)
		c[i] ^= 0x01
		_, err := b.Open(c, []byte("aad"))
		require.ErrorIs(t, err, sealbox.ErrOpen, "byte %d", i)
	}
}

func TestRotationOpensOldKey(t *testing.T) {
	old, err := sealbox.ParseKeys("k1:"+key(1), "k1")
	require.NoError(t, err)
	sealed, err := old.Seal([]byte("Ana"), []byte("aad"))
	require.NoError(t, err)

	rotated, err := sealbox.ParseKeys("k1:"+key(1)+",k2:"+key(2), "k2")
	require.NoError(t, err)
	got, err := rotated.Open(sealed, []byte("aad"))
	require.NoError(t, err)
	require.Equal(t, "Ana", string(got))

	fresh, err := rotated.Seal([]byte("Bo"), []byte("aad"))
	require.NoError(t, err)
	require.Equal(t, byte(2), fresh[1]) // len("k2")
	require.Equal(t, "k2", string(fresh[2:4]))

	// a box without the old key cannot open it
	onlyNew, err := sealbox.ParseKeys("k2:"+key(2), "k2")
	require.NoError(t, err)
	_, err = onlyNew.Open(sealed, []byte("aad"))
	require.ErrorIs(t, err, sealbox.ErrOpen)
}

func TestParseKeysRejects(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16))
	cases := map[string][2]string{
		"short key":       {"k1:" + short, "k1"},
		"bad base64":      {"k1:!!!notbase64!!!", "k1"},
		"missing current": {"k1:" + key(1), ""},
		"unknown current": {"k1:" + key(1), "k9"},
		"bad id upper":    {"K1:" + key(1), "K1"},
		"bad id empty":    {":" + key(1), ""},
		"bad id long":     {strings.Repeat("a", 17) + ":" + key(1), strings.Repeat("a", 17)},
		"empty spec":      {"", "k1"},
		"no colon":        {"k1" + key(1), "k1"},
		"duplicate id":    {"k1:" + key(1) + ",k1:" + key(2), "k1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := sealbox.ParseKeys(c[0], c[1])
			require.Error(t, err)
		})
	}
}

func TestParseKeysErrorDoesNotLeakKeyMaterial(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
	for _, spec := range []string{
		"k1:" + short,
		"k1:" + key(3) + ",k1:" + key(4),
		"K1:" + key(5),
		"k1:" + key(6) + "!!",
	} {
		_, err := sealbox.ParseKeys(spec, "k1")
		require.Error(t, err)
		for _, part := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ':' }) {
			if len(part) > 8 {
				require.NotContains(t, err.Error(), part)
			}
		}
	}
}

func TestParseKeysDoesNotEchoCurrent(t *testing.T) {
	leaky := key(9)
	_, err := sealbox.ParseKeys("k1:"+key(1), leaky)
	require.Error(t, err)
	require.NotContains(t, err.Error(), leaky)
}

func TestSealNonceUnique(t *testing.T) {
	b := newBox(t)
	seen := map[string]struct{}{}
	for range 1000 {
		s, err := b.Seal([]byte("same"), []byte("aad"))
		require.NoError(t, err)
		seen[string(s)] = struct{}{}
	}
	require.Len(t, seen, 1000)
}

func FuzzOpen(f *testing.F) {
	b, err := sealbox.ParseKeys("k1:"+key(1), "k1")
	if err != nil {
		f.Fatal(err)
	}
	good, _ := b.Seal([]byte("Ana"), []byte("aad"))
	f.Add(good, []byte("aad"))
	f.Add([]byte{}, []byte{})
	f.Add([]byte{1}, []byte{})
	f.Add([]byte{1, 200, 'k'}, []byte{})
	f.Add([]byte{1, 2, 'k', '1'}, []byte{})
	f.Fuzz(func(t *testing.T, sealed, aad []byte) {
		_, err := b.Open(sealed, aad)
		if err != nil && !errors.Is(err, sealbox.ErrOpen) {
			t.Fatalf("unexpected error type: %v", err)
		}
	})
}
