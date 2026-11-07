package sim

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// viewerRetry spaces reconnects; the gateway limits viewer sessions to 10 a minute per IP.
const viewerRetry = 8 * time.Second

// trackingToken is the last path segment of the delivery's tracking link (/t/<token>).
func trackingToken(trackingURL string) (string, error) {
	u, err := url.Parse(trackingURL)
	if err != nil {
		return "", errors.New("sim: bad tracking url")
	}
	seg := u.Path[strings.LastIndex(u.Path, "/")+1:]
	if seg == "" {
		return "", errors.New("sim: tracking url without token")
	}
	return seg, nil
}

// runViewer is the one headless viewer of a run: it opens a viewer session with the tracking link's token and reads
// the SSE stream, as the tracking page does, until ctx ends. It reconnects after a failure or a token expiry.
// Nothing it reads is kept.
func runViewer(ctx context.Context, edge gw, stream *http.Client, token string, log *slog.Logger) {
	for ctx.Err() == nil {
		if err := viewOnce(ctx, edge, stream, token); err != nil && ctx.Err() == nil {
			log.Debug("sim viewer stopped", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(viewerRetry):
		}
	}
}

func viewOnce(ctx context.Context, edge gw, stream *http.Client, token string) error {
	var sess struct {
		ViewerToken string `json:"viewer_token"`
	}
	st, err := edge.do(ctx, "POST", "/v1/tracking/session", "", map[string]string{"token": token}, &sess)
	if err != nil {
		return err
	}
	if st != 200 || sess.ViewerToken == "" {
		return fmt.Errorf("sim: viewer session: status %d", st)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", edge.base+"/v1/tracking/stream/sse", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+sess.ViewerToken)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := stream.Do(req)
	if err != nil {
		return errors.New("sim: viewer stream: connect failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("sim: viewer stream: status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() { // lines are read and dropped
	}
	return nil
}
