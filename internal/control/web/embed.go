// Package web embeds the built front-end (web/dist, synced here by `make web`)
// into the binary.
package web

import "embed"

// Dist holds the embedded front-end build output.
//
//go:embed all:dist
var Dist embed.FS
