package domain

import "strings"

// Branding is a tenant's viewer branding. The logo bytes live in the store;
// LogoSHA256 (hex) names them in the logo URL.
type Branding struct {
	DisplayName, PrimaryColor, Theme, LogoSHA256, LogoContentType string
}

// ViewerSettings are the tenant settings the hosted tracking page reads.
type ViewerSettings struct {
	// EmbedOrigins may frame the hosted page; empty means no one may.
	EmbedOrigins []string
	// Branding is nil for the DT defaults.
	Branding *Branding
}

// FrameAncestors is the frame-ancestors source list for origins: 'none' when
// there is none. An entry that could add a directive or another source (it
// holds whitespace, ';', ',', a quote or a control character) is dropped, so
// a bad stored value can never rewrite the policy.
func FrameAncestors(origins []string) string {
	var ok []string
	for _, o := range origins {
		if o != "" && !strings.ContainsFunc(o, func(r rune) bool {
			return r <= ' ' || r == 0x7f || r == ';' || r == ',' || r == '\'' || r == '"'
		}) {
			ok = append(ok, o)
		}
	}
	if len(ok) == 0 {
		return "'none'"
	}
	return strings.Join(ok, " ")
}

// PageOrigins are the origins the hosted page loads from and talks to. An
// empty CDN means the page's own origin ('self'); an empty API or Tiles is
// left out of connect-src.
type PageOrigins struct {
	CDN, API, Tiles string
	Extra           []string
}

// PageCSP is the Content-Security-Policy of the hosted tracking page.
func PageCSP(frameAncestors string, o PageOrigins) string {
	cdn := o.CDN
	if cdn == "" {
		cdn = "'self'"
	}
	connect := []string{}
	for _, s := range append([]string{o.API, o.Tiles, o.CDN}, o.Extra...) {
		if s != "" {
			connect = append(connect, s)
		}
	}
	if o.CDN == "" {
		connect = append(connect, "'self'")
	}
	return "default-src 'none'; script-src " + cdn + "; style-src " + cdn + " 'unsafe-inline'; img-src " + cdn +
		" 'self' data: blob:; connect-src " + strings.Join(connect, " ") + "; worker-src blob:; font-src " + cdn +
		"; base-uri 'none'; form-action 'none'; frame-ancestors " + frameAncestors
}
