// Package migrations embeds the goose SQL migration files so the
// application can run them at startup without relying on the working
// directory or a separate CLI.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
