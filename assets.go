// Package tictactoe embeds the web assets so the deliverable is one static binary.
package tictactoe

import "embed"

//go:embed web/templates
var TemplatesFS embed.FS

//go:embed web/static
var StaticFS embed.FS
