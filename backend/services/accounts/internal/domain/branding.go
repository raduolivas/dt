package domain

import (
	"bytes"
	"fmt"
	"image"
	"image/png" // registers the PNG decoder for image.DecodeConfig
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/webp"
)

// Branding limits.
const (
	MaxDisplayNameChars = 60
	MaxLogoBytes        = 100 * 1024
	MaxLogoPixels       = 512
	MaxEmbedOrigins     = 20
)

// Logo content types, derived from the bytes.
const (
	LogoPNG  = "image/png"
	LogoWebP = "image/webp"
)

var (
	colorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	labelRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$`)

	pngMagic = []byte("\x89PNG\r\n\x1a\n")

	knownThemes = []string{"light", "dark", "auto"}
)

// Branding is a tenant's look on the hosted tracking page. Empty fields mean
// DT defaults. The Logo is PNG or WebP; LogoContentType is derived from its
// bytes by Normalized, never trusted from the caller.
type Branding struct {
	DisplayName, PrimaryColor, Theme string
	Logo                             []byte
	LogoContentType                  string
}

// IsZero reports whether every field is empty.
func (b Branding) IsZero() bool {
	return b.DisplayName == "" && b.PrimaryColor == "" && b.Theme == "" && len(b.Logo) == 0
}

// Normalized returns b with LogoContentType derived from the logo bytes
// (empty when there is no logo or the bytes are not a known image).
func (b Branding) Normalized() Branding {
	b.LogoContentType = logoType(b.Logo)
	return b
}

func logoType(logo []byte) string {
	switch {
	case bytes.HasPrefix(logo, pngMagic):
		return LogoPNG
	case len(logo) >= 12 && string(logo[:4]) == "RIFF" && string(logo[8:12]) == "WEBP":
		return LogoWebP
	}
	return ""
}

// Validate reports ErrInvalidArgument naming the first offending field.
func (b Branding) Validate() error {
	if utf8.RuneCountInString(b.DisplayName) > MaxDisplayNameChars {
		return fmt.Errorf("%w: branding.display_name must be at most %d characters", ErrInvalidArgument, MaxDisplayNameChars)
	}
	if b.PrimaryColor != "" && !colorRe.MatchString(b.PrimaryColor) {
		return fmt.Errorf("%w: branding.primary_color must be #RRGGBB", ErrInvalidArgument)
	}
	if b.Theme != "" && !contains(knownThemes, b.Theme) {
		return fmt.Errorf("%w: branding.theme must be light, dark or auto", ErrInvalidArgument)
	}
	return validateLogo(b.Logo)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func validateLogo(logo []byte) error {
	if len(logo) == 0 {
		return nil
	}
	if len(logo) > MaxLogoBytes {
		return fmt.Errorf("%w: branding.logo must be at most 100 KB", ErrInvalidArgument)
	}
	var cfg image.Config
	var err error
	switch logoType(logo) {
	case LogoPNG:
		cfg, err = png.DecodeConfig(bytes.NewReader(logo))
	case LogoWebP:
		cfg, err = webp.DecodeConfig(bytes.NewReader(logo))
	default:
		return fmt.Errorf("%w: branding.logo must be a PNG or WebP image", ErrInvalidArgument)
	}
	if err != nil {
		return fmt.Errorf("%w: branding.logo is not a valid image", ErrInvalidArgument)
	}
	if cfg.Width > MaxLogoPixels || cfg.Height > MaxLogoPixels {
		return fmt.Errorf("%w: branding.logo must be at most %dx%d pixels", ErrInvalidArgument, MaxLogoPixels, MaxLogoPixels)
	}
	return nil
}

// ValidateEmbedOrigin accepts https://host[:port] with no path, query or
// fragment; a leading "*." subdomain wildcard (https://*.example.com) is the
// only wildcard allowed.
func ValidateEmbedOrigin(o string) error {
	bad := func(why string) error {
		return fmt.Errorf("%w: embed_origins entry %q: %s", ErrInvalidArgument, o, why)
	}
	rest, ok := strings.CutPrefix(o, "https://")
	if !ok {
		return bad("must start with https://")
	}
	host, port, hasPort := strings.Cut(rest, ":")
	if hasPort {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || port != strconv.Itoa(n) {
			return bad("invalid port")
		}
	}
	wild := false
	if h, ok := strings.CutPrefix(host, "*."); ok {
		host, wild = h, true
	}
	labels := strings.Split(host, ".")
	if wild && len(labels) < 2 {
		return bad("wildcard needs a registrable domain")
	}
	for _, l := range labels {
		if len(l) > 63 || !labelRe.MatchString(l) {
			return bad("want https://host[:port] with an optional leading *. and no path, query or fragment")
		}
	}
	return nil
}

func validateEmbedOrigins(origins []string) error {
	if len(origins) > MaxEmbedOrigins {
		return fmt.Errorf("%w: embed_origins allows at most %d entries", ErrInvalidArgument, MaxEmbedOrigins)
	}
	for i, o := range origins {
		if err := ValidateEmbedOrigin(o); err != nil {
			return err
		}
		if contains(origins[:i], o) {
			return fmt.Errorf("%w: duplicate embed_origins entry %q", ErrInvalidArgument, o)
		}
	}
	return nil
}
