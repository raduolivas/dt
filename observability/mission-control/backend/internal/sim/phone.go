package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// gw is a minimal JSON client for DT's public API. Errors carry the method, path and status only: never a body,
// a token or a key.
type gw struct {
	base       string
	hc         *http.Client
	idempotent bool // send an Idempotency-Key on writes (the partner API)
}

func (g gw) do(ctx context.Context, method, path, bearer string, in, out any) (int, error) {
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, rd)
	if err != nil {
		return 0, errors.New("sim: bad request")
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if g.idempotent && method != http.MethodGet {
		req.Header.Set("Idempotency-Key", "mc-sim-"+randHex(12)) // the partner API requires one on writes
	}
	resp, err := g.hc.Do(req)
	if err != nil {
		// the url error text would name the host only, but keep it free of anything request-shaped
		return 0, fmt.Errorf("sim: %s %s: unreachable", method, path)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("sim: %s %s: undecodable answer", method, path)
		}
	}
	return resp.StatusCode, nil
}

// partner makes the sandbox-key calls (the partner API).
type partner struct {
	g   gw
	key string
}

func (p *partner) call(ctx context.Context, method, path string, in, out any, want ...int) error {
	code, err := p.g.do(ctx, method, "/v1"+path, p.key, in, out)
	if err != nil {
		return err
	}
	for _, w := range want {
		if code == w {
			return nil
		}
	}
	return fmt.Errorf("sim: %s /v1%s: status %d", method, path, code)
}

func (p *partner) upsertCourier(ctx context.Context, ext string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := p.call(ctx, "PUT", "/couriers/"+ext, map[string]string{"display_name": "Sim Courier", "vehicle": "e_bike"}, &out, 200, 201); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("sim: courier answer without id")
	}
	return out.ID, nil
}

func (p *partner) createDelivery(ctx context.Context, ref string, from, to LatLng) (id, trackingURL string, err error) {
	loc := func(l LatLng) map[string]float64 { return map[string]float64{"lat": l.Lat, "lng": l.Lng} }
	var out struct {
		ID       string `json:"id"`
		Tracking struct {
			URL string `json:"url"`
		} `json:"tracking"`
	}
	// A delivery created right after the tenant settings can race them to the deliveries service: retry briefly.
	for range 20 {
		err = p.call(ctx, "POST", "/deliveries", map[string]any{
			"external_id": ref, "delivery_model": "direct",
			"pickup":   map[string]any{"location": loc(from), "name": "Sim Kitchen"},
			"dropoff":  map[string]any{"location": loc(to), "label": "Sim Door"},
			"customer": map[string]string{"first_name": "Sim"},
		}, &out, 201)
		if err == nil || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	return out.ID, out.Tracking.URL, err
}

func (p *partner) assign(ctx context.Context, deliveryID, courierID string) error {
	return p.call(ctx, "POST", "/deliveries/"+deliveryID+"/assignment", map[string]string{"courier_id": courierID}, nil, 200)
}

// linkCode opens a link-mode courier session and returns the one-time code from its link.
func (p *partner) linkCode(ctx context.Context, courierID string) (string, error) {
	var out struct {
		LinkURL string `json:"link_url"`
	}
	if err := p.call(ctx, "POST", "/courier-sessions", map[string]string{"courier_id": courierID, "mode": "link"}, &out, 201); err != nil {
		return "", err
	}
	_, code, ok := strings.Cut(out.LinkURL, "/s/")
	if !ok || code == "" {
		return "", errors.New("sim: unexpected link_url")
	}
	return code, nil
}

// phone is the fake courier phone: it holds a courier session and posts through the gateway courier API with the
// courier's own JWT, as the courier page does.
type phone struct {
	g       gw
	mu      sync.Mutex
	access  string
	refresh string
	expires time.Time
}

type tokenPair struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

func (ph *phone) store(t tokenPair) error {
	if t.AccessToken == "" {
		return errors.New("sim: token answer without access token")
	}
	ph.mu.Lock()
	defer ph.mu.Unlock()
	ph.access = t.AccessToken
	if t.RefreshToken != "" {
		ph.refresh = t.RefreshToken
	}
	ph.expires = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return nil
}

func (ph *phone) exchange(ctx context.Context, code string) error {
	var t tokenPair
	st, err := ph.g.do(ctx, "POST", "/v1/courier/token", "", map[string]string{"grant_type": "link_code", "code": code}, &t)
	if err != nil {
		return err
	}
	if st != 200 {
		return fmt.Errorf("sim: courier token exchange: status %d", st)
	}
	return ph.store(t)
}

// accessToken returns the access token, refreshing it first when it expires within a minute.
func (ph *phone) accessToken(ctx context.Context) (string, error) {
	ph.mu.Lock()
	tok, refresh, exp := ph.access, ph.refresh, ph.expires
	ph.mu.Unlock()
	if time.Until(exp) > time.Minute || refresh == "" {
		return tok, nil
	}
	var t tokenPair
	st, err := ph.g.do(ctx, "POST", "/v1/courier/token", "", map[string]string{"grant_type": "refresh_token", "refresh_token": refresh}, &t)
	if err != nil {
		return tok, err
	}
	if st != 200 {
		return tok, fmt.Errorf("sim: courier token refresh: status %d", st)
	}
	if err := ph.store(t); err != nil {
		return tok, err
	}
	ph.mu.Lock()
	defer ph.mu.Unlock()
	return ph.access, nil
}

type fix struct {
	T        string  `json:"t"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Accuracy float64 `json:"accuracy_m"`
	Speed    float64 `json:"speed_mps"`
	Heading  float64 `json:"heading_deg"`
}

// postFix sends one fix. corrupt sends a token with a damaged signature (scenario 2). It returns the HTTP status.
func (ph *phone) postFix(ctx context.Context, f fix, corrupt bool) (int, error) {
	tok, err := ph.accessToken(ctx)
	if err != nil && tok == "" {
		return 0, err
	}
	if corrupt {
		tok = corruptJWT(tok)
	}
	return ph.g.do(ctx, "POST", "/v1/courier/locations", tok, map[string]any{"fixes": []fix{f}}, nil)
}

func (ph *phone) confirmPickup(ctx context.Context, deliveryID, eventID string) error {
	tok, _ := ph.accessToken(ctx)
	st, err := ph.g.do(ctx, "POST", "/v1/courier/deliveries/"+deliveryID+"/events", tok,
		map[string]string{"event_id": eventID, "type": "pickup_confirmed", "occurred_at": time.Now().UTC().Format(time.RFC3339)}, nil)
	if err != nil {
		return err
	}
	if st != 200 {
		return fmt.Errorf("sim: pickup_confirmed: status %d", st)
	}
	return nil
}

// corruptJWT damages a character inside the signature (not the last one: its low bits are padding), so the token
// still looks like a JWT and the gateway answers 401 rather than "missing".
func corruptJWT(tok string) string {
	b := []byte(tok)
	if len(b) < 8 {
		return tok + "x"
	}
	i := len(b) - 6
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	return string(b)
}
