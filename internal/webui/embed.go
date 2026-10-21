// Package webui contains the offline browser interface bundled into figmobil.
package webui

import "embed"

// Files contains only local application assets. The server exposes an explicit allowlist.
//
//go:embed index.html app.js styles.css *.mjs
var Files embed.FS
