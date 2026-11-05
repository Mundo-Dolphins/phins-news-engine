// Package web embeds the built admin SPA.
package web

import "embed"

// Dist holds the built single-page app under the "dist" directory.
//
//go:embed all:dist
var Dist embed.FS
