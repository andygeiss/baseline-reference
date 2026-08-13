// Package reference embeds the web assets so the deliverable is one static binary.
package reference

import "embed"

//go:embed web/templates
var TemplatesFS embed.FS

//go:embed web/static
var StaticFS embed.FS
