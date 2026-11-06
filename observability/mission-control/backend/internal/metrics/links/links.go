// Package links renders "open in..." deep links from URL templates (MC-14).
package links

import (
	"net/url"
	"regexp"
	"strings"
)

// Link is one rendered deep link.
type Link struct {
	Kind  string
	Label string
	URL   string
}

// Kinds and their labels.
var labels = map[string]string{
	"grafana_trace":   "Open trace in Grafana",
	"grafana_logs":    "Open logs in Grafana",
	"xray_trace":      "Open trace in X-Ray",
	"cloudwatch_logs": "Open logs in CloudWatch",
}

// Kinds lists the supported link kinds.
func Kinds() []string {
	return []string{"grafana_trace", "grafana_logs", "xray_trace", "cloudwatch_logs"}
}

var placeholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// SafeTemplate reports whether a template starts with http:// or https://
// (case-insensitive), so it can never render a javascript: or data: URL.
func SafeTemplate(t string) bool {
	l := strings.ToLower(t)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// Render returns the link for kind when its template is configured, safe, and
// every {var} in it has a value. Values are URL-escaped.
func Render(templates map[string]string, kind string, vars map[string]string) []Link {
	label, ok := labels[kind]
	tpl := templates[kind]
	if !ok || tpl == "" || !SafeTemplate(tpl) {
		return nil
	}
	missing := false
	out := placeholder.ReplaceAllStringFunc(tpl, func(m string) string {
		v, ok := vars[m[1:len(m)-1]]
		// "." and ".." are dot segments a browser would resolve away.
		if !ok || v == "" || v == "." || v == ".." {
			missing = true
			return ""
		}
		// QueryEscape writes a space as +; %20 is right in paths and queries.
		return strings.ReplaceAll(url.QueryEscape(v), "+", "%20")
	})
	if missing {
		return nil
	}
	if u, err := url.Parse(out); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	return []Link{{Kind: kind, Label: label, URL: out}}
}
