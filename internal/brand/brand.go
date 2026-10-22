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
	// AddonPkg is the Slidev addon with the "Export as .sptx" button (addon/). The runtime bundles it
	// (build/addon.sh), so a talk may name it in addons:, as AddonPkg or AddonName, without that counting
	// as code, and pack never bundles a copy of it.
	AddonPkg  = "slidev-addon-stagepack"
	AddonName = "stagepack"
	// ViewerEnv is set to 1 in Slidev's environment when the viewer starts it; the addon then does nothing.
	ViewerEnv = "STAGEPACK_VIEWER"
)
