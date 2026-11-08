// Package migrations embeds the Mission Control schema (golang-migrate format).
package migrations

import "embed"

// FS holds the *.sql migrations in its root.
//
//go:embed *.sql
var FS embed.FS
