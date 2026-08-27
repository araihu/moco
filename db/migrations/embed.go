// Package migrations embeds Mocó's SQLite migrations for server startup.
package migrations

import "embed"

// Files contains every up and down migration consumed by golang-migrate.
//
//go:embed *.sql
var Files embed.FS
