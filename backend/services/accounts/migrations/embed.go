// Package migrations embeds the accounts schema migrations (golang-migrate
// format), applied by bootstrap before the service starts.
package migrations

import "embed"

// FS holds the *.sql migrations in its root.
//
//go:embed *.sql
var FS embed.FS
