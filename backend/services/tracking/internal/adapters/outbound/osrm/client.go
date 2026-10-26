// Package osrm is the routing adapter: an HTTP client for osrm-routed
// (/route and /match), one base URL per profile.
package osrm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// DefaultTimeout is the per-request timeout: tracking falls back to a
// straight-line estimate rather than wait longer (MVP spec §8.5).
const DefaultTimeout = 800 * time.Millisecond

// maxBody caps a response.
const maxBody = 8 << 20

// osrmProfile is the name in the request path. osrm-routed serves one
// profile per process and ignores it; it documents the request.
var osrmProfile = map[string]string{"car": "driving", "bike": "cycling"}

// Client talks to osrm-routed.
type Client struct {
	urls map[string]string
	http *http.Client
}

var _ app.Router = (*Client)(nil)

// New returns a client for the given base URLs by profile ("car", "bike").
// timeout bounds each request.
func New(baseURLs map[string]string, timeout time.Duration) *Client {
	urls := make(map[string]string, len(baseURLs))
	for p, u := range baseURLs {
		urls[p] = strings.TrimRight(u, "/")
	}
	return &Client{urls: urls, http: &http.Client{Timeout: timeout}}
}

type geometry struct {
	Coordinates [][2]float64 `json:"coordinates"`
}

type response struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Routes  []struct {
		Duration float64  `json:"duration"`
		Geometry geometry `json:"geometry"`
	} `json:"routes"`
	Matchings []struct {
		Geometry geometry `json:"geometry"`
	} `json:"matchings"`
}

// Route implements app.Router.
func (c *Client) Route(ctx context.Context, profile string, pts ...domain.LatLng) ([]domain.LatLng, int, error) {
	if len(pts) < 2 {
		return nil, 0, errors.New("osrm: a route needs at least two points")
	}
	coords := make([]string, len(pts))
	for i, p := range pts {
		coords[i] = coord(p)
	}
	var r response
	q := url.Values{"overview": {"full"}, "geometries": {"geojson"}, "steps": {"false"}}
	if err := c.get(ctx, profile, "route", strings.Join(coords, ";"), q, &r); err != nil {
		return nil, 0, err
	}
	if len(r.Routes) == 0 || len(r.Routes[0].Geometry.Coordinates) < 2 {
		return nil, 0, errors.New("osrm: route: empty response")
	}
	return latLngs(r.Routes[0].Geometry), int(math.Round(r.Routes[0].Duration)), nil
}

// Match implements app.Router. The fixes must be in time order.
func (c *Client) Match(ctx context.Context, profile string, fixes []domain.Fix) ([]domain.LatLng, error) {
	if len(fixes) < 2 {
		return nil, errors.New("osrm: a match needs at least two fixes")
	}
	coords := make([]string, len(fixes))
	ts := make([]string, len(fixes))
	rad := make([]string, len(fixes))
	for i, f := range fixes {
		coords[i] = coord(f.Pos)
		ts[i] = strconv.FormatInt(f.T.Unix(), 10)
		acc := f.AccuracyM
		if acc <= 0 {
			acc = 10
		}
		rad[i] = strconv.FormatFloat(acc, 'f', -1, 64)
	}
	// Semicolons separate the values and are valid in a query, so the query
	// is built by hand rather than escaped.
	extra := "geometries=geojson&overview=full&timestamps=" + strings.Join(ts, ";") + "&radiuses=" + strings.Join(rad, ";")
	var r response
	if err := c.getRaw(ctx, profile, "match", strings.Join(coords, ";"), extra, &r); err != nil {
		return nil, err
	}
	var out []domain.LatLng
	for _, m := range r.Matchings {
		out = append(out, latLngs(m.Geometry)...)
	}
	if len(out) == 0 {
		return nil, errors.New("osrm: match: empty response")
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, profile, service, coords string, q url.Values, out *response) error {
	return c.getRaw(ctx, profile, service, coords, q.Encode(), out)
}

func (c *Client) getRaw(ctx context.Context, profile, service, coords, rawQuery string, out *response) error {
	base, ok := c.urls[profile]
	if !ok || base == "" {
		return fmt.Errorf("osrm: no server for profile %q", profile)
	}
	u := fmt.Sprintf("%s/%s/v1/%s/%s?%s", base, service, osrmProfile[profile], coords, rawQuery)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("osrm: %s: %w", service, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("osrm: %s: %w", service, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("osrm: %s: read: %w", service, err)
	}
	// osrm-routed answers 400 with a JSON code for NoRoute and the like.
	if jerr := json.Unmarshal(body, out); jerr != nil || resp.StatusCode/100 != 2 {
		if jerr == nil && out.Code != "" {
			return fmt.Errorf("osrm: %s: %s %s", service, out.Code, out.Message)
		}
		return fmt.Errorf("osrm: %s: HTTP %d", service, resp.StatusCode)
	}
	if out.Code != "Ok" {
		return fmt.Errorf("osrm: %s: %s %s", service, out.Code, out.Message)
	}
	return nil
}

// coord is a point as OSRM wants it: lng,lat.
func coord(p domain.LatLng) string {
	return strconv.FormatFloat(p.Lng, 'f', 6, 64) + "," + strconv.FormatFloat(p.Lat, 'f', 6, 64)
}

func latLngs(g geometry) []domain.LatLng {
	out := make([]domain.LatLng, len(g.Coordinates))
	for i, c := range g.Coordinates {
		out[i] = domain.LatLng{Lat: c[1], Lng: c[0]}
	}
	return out
}
