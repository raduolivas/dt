package testkit

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

// NATSImage matches the local stack.
const NATSImage = "nats:2.12-alpine"

// StartNATS starts a JetStream-enabled server and returns a nats:// URL. It
// terminates the container when t finishes and skips under -short.
func StartNATS(t testing.TB) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker (run without -short)")
	}
	ctx := context.Background()
	c, err := tcnats.Run(ctx, NATSImage, testcontainers.WithCmdArgs("-js"))
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("start nats: %v", err)
	}
	url, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("nats url: %v", err)
	}
	return url
}
