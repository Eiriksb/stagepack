// Package brand holds the product's names. Every other file refers to these
// constants instead of spelling the strings itself.
package brand

const (
	Name         = "Stagepack"
	Ext          = ".sptx"
	ManifestName = "stagepack.json"
	ProgID       = "Stagepack.Talk"
	// AppID names the program on Linux desktops: the .desktop file, the icon and
	// the MIME package, and the install folder in ~/.local/share.
	AppID    = "stagepack"
	MIMEType = "application/x-stagepack"
)
