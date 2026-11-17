// Package web embeds the frontend so the binary is self-contained and works offline.
// web/styles/ holds the Tailwind source and is not embedded; `make css` compiles it into
// web/assets/css/app.css, which is committed.
package web

import "embed"

//go:embed index.html assets
var FS embed.FS
