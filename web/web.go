// Package web embeds the static SPA files (index.html, app.js, styles.css)
// so they ship inside the Go binary — no separate web server, no build step.
package web

import "embed"

//go:embed index.html app.js styles.css
var Files embed.FS
