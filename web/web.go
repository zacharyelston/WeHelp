// Package web embeds the static SPA files (index.html, app.js, staff.js,
// forms.js and forms-eval.js for the form builder, styles.css)
// so they ship inside the Go binary — no separate web server, no build step.
package web

import "embed"

//go:embed index.html app.js staff.js forms.js forms-eval.js styles.css
var Files embed.FS
