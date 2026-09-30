// Package migrations embeds the SQL migration files so binaries can carry them.
package migrations

import "embed"

// FS holds all versioned migration files (*.up.sql / *.down.sql).
//
//go:embed *.sql
var FS embed.FS
