package bootstrap_test

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/testkit"
)

func TestNATSDrainIsBounded(t *testing.T) {
	url := testkit.StartNATS(t)
	nc, err := nats.Connect(url, nats.DrainTimeout(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)

	// A consumer that never finishes its message: a plain Drain waits for it.
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	started := make(chan struct{}, 1)
	if _, err := nc.Subscribe("stall", func(*nats.Msg) {
		started <- struct{}{}
		<-stall
	}); err != nil {
		t.Fatal(err)
	}
	if err := nc.Publish("stall", []byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = nc.Flush()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}

	begin := time.Now()
	bootstrap.DrainNATS(nc)
	if took := time.Since(begin); took > bootstrap.NATSDrainTimeout+time.Second {
		t.Fatalf("drain took %v, want at most %v", took, bootstrap.NATSDrainTimeout+time.Second)
	}
	if !nc.IsClosed() {
		t.Fatal("connection still open after drain")
	}
}
