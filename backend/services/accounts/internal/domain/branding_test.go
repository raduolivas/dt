package domain

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
)

// tinyWebP is a 1x1 lossless WebP.
var tinyWebP, _ = base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestEmbedOriginRules(t *testing.T) {
	for _, o := range []string{"https://shop.example.com", "https://shop.example.com:8443", "https://*.example.com"} {
		if err := ValidateEmbedOrigin(o); err != nil {
			t.Errorf("%q rejected: %v", o, err)
		}
	}
	for _, o := range []string{"http://x.com", "https://x.com/path", "https://x.com/", "https://x.com?q", "https://x.com#f",
		"https://*", "https://a.*.com", "*.example.com", "https://*.com:", "", "https://", "https://x.com:99999",
		"https://us er@x.com", "https://*.*.example.com"} {
		err := ValidateEmbedOrigin(o)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%q: err = %v, want ErrInvalidArgument", o, err)
		}
	}
	s := DefaultTenantSettings()
	for i := range 20 {
		s.EmbedOrigins = append(s.EmbedOrigins, fmt.Sprintf("https://s%d.example.com", i))
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("20 entries: %v", err)
	}
	s.EmbedOrigins = append(s.EmbedOrigins, "https://s20.example.com")
	if err := s.Validate(); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("21 entries: err = %v", err)
	}
	s.EmbedOrigins = []string{"https://a.com", "https://a.com"}
	if err := s.Validate(); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("duplicates: err = %v", err)
	}
	s.EmbedOrigins = []string{"http://a.com"}
	if err := s.Validate(); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad entry via settings: err = %v", err)
	}
}

func TestBrandingRules(t *testing.T) {
	ok := Branding{DisplayName: "Acme", PrimaryColor: "#A1b2C3", Theme: "auto"}
	with := func(f func(*Branding)) Branding { b := ok; f(&b); return b }
	bad := map[string]Branding{
		"61 chars":    with(func(b *Branding) { b.DisplayName = strings.Repeat("a", 61) }),
		"short color": with(func(b *Branding) { b.PrimaryColor = "#12345" }),
		"named color": with(func(b *Branding) { b.PrimaryColor = "red" }),
		"no hash":     with(func(b *Branding) { b.PrimaryColor = "123456" }),
		"theme":       with(func(b *Branding) { b.Theme = "night" }),
		"svg":         with(func(b *Branding) { b.Logo = []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`) }),
		"100KB+1":     with(func(b *Branding) { b.Logo = append(pngBytes(t, 4, 4), make([]byte, 100*1024)...) }),
		"513x10":      with(func(b *Branding) { b.Logo = pngBytes(t, 513, 10) }),
		"10x513":      with(func(b *Branding) { b.Logo = pngBytes(t, 10, 513) }),
		"truncated":   with(func(b *Branding) { b.Logo = pngBytes(t, 4, 4)[:12] }),
	}
	for name, b := range bad {
		if err := b.Validate(); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want ErrInvalidArgument", name, err)
		}
	}
	if err := with(func(b *Branding) { b.DisplayName = strings.Repeat("é", 60) }).Validate(); err != nil {
		t.Errorf("60 runes: %v", err)
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("no logo: %v", err)
	}
	if err := (Branding{}).Validate(); err != nil {
		t.Errorf("empty: %v", err)
	}

	big := pngBytes(t, 512, 512)
	if len(big) > 100*1024 {
		t.Fatalf("fixture too big: %d", len(big))
	}
	b := with(func(b *Branding) { b.Logo, b.LogoContentType = big, "image/svg+xml" })
	if err := b.Validate(); err != nil {
		t.Fatalf("512x512 png: %v", err)
	}
	if got := b.Normalized(); got.LogoContentType != "image/png" {
		t.Errorf("png type = %q", got.LogoContentType)
	}
	w := with(func(b *Branding) { b.Logo, b.LogoContentType = tinyWebP, "image/png" })
	if err := w.Validate(); err != nil {
		t.Fatalf("webp: %v", err)
	}
	if got := w.Normalized(); got.LogoContentType != "image/webp" {
		t.Errorf("webp type = %q", got.LogoContentType)
	}
	if got := ok.Normalized(); got.LogoContentType != "" {
		t.Errorf("no logo type = %q", got.LogoContentType)
	}
}
