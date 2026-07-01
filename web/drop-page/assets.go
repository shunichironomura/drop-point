package droppage

import "embed"

// Files contains the sender-facing browser encryption page assets.
//
//go:embed index.html styles.css app.js
var Files embed.FS
