package webui

import "embed"

// staticFS embeds all frontend static assets (HTML/JS/CSS) at compile time.
//
//go:embed static/*.html static/*.js static/*.css
var staticFS embed.FS
