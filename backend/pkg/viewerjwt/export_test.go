package viewerjwt

import "time"

// ClientTimeout exposes the verifier's HTTP client timeout to tests.
func (v *Verifier) ClientTimeout() time.Duration { return v.client.Timeout }
