// Package assets holds the program icon, drawn by mkicon.go, for the parts that
// install it (the Linux desktop entry; Windows takes it from the .syso).
package assets

import _ "embed"

// Icon is stagepack.ico: PNG images of 256, 48, 32 and 16 px.
//
//go:embed stagepack.ico
var Icon []byte
