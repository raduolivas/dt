package testkit_test

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/raduolivas/dt/backend/pkg/testkit"
)

func TestStartNATSHasJetStream(t *testing.T) {
	nc, err := nats.Connect(testkit.StartNATS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	// Fails if JetStream is not enabled.
	if _, err := js.AccountInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
}
