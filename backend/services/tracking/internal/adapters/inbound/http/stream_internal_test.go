package httpapi

import (
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
)

func TestExpiryAfterLeaveArmsNoTimer(t *testing.T) {
	h := NewHub(Deps{})
	s := newStream(viewerjwt.Claims{DeliveryID: "del_1", TenantID: "ten_a", Env: "test", LinkID: "tl_1"})
	h.leave(s) // the stream's handler is gone (also without join)
	s.expireAt(time.Now().Add(time.Hour))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expiry != nil {
		t.Fatal("an expiry timer was armed for a stream that already left")
	}
}
