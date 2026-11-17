// Package migrations embeds the versioned SQL schema migrations.
//
// Files are named NNNN_description.sql and are append-only: never edit a
// migration that may have been applied somewhere.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
