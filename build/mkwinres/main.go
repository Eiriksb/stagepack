// Command mkwinres writes the go-winres config (icon, manifest, version info)
// for the Windows program, with the product's names taken from internal/brand.
// Usage: go run ./build/mkwinres OUTDIR ICON.ico
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"stagepack/internal/brand"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mkwinres OUTDIR ICON.ico")
		os.Exit(2)
	}
	out, icon := os.Args[1], os.Args[2]
	rel, err := filepath.Rel(out, icon)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	exe := strings.ToLower(brand.Name) + ".exe"
	cfg := map[string]any{
		"RT_GROUP_ICON": map[string]any{"APP": map[string]any{"0000": filepath.ToSlash(rel)}},
		"RT_MANIFEST": map[string]any{"#1": map[string]any{"0409": map[string]any{
			"description": brand.Name,
			"minimum-os":  "win10",
			// Without a manifest, Windows guesses from the name that a "setup" program needs administrator rights.
			"execution-level":        "as invoker",
			"dpi-awareness":          "per monitor v2",
			"long-path-aware":        true,
			"use-common-controls-v6": true,
		}}},
		"RT_VERSION": map[string]any{"#1": map[string]any{"0000": map[string]any{
			"fixed": map[string]any{"file_version": "0.1.0.0", "product_version": "0.1.0.0"},
			"info": map[string]any{"0409": map[string]any{
				"FileDescription":  brand.Name + " talk viewer",
				"FileVersion":      "0.1.0.0",
				"InternalName":     brand.Name,
				"OriginalFilename": exe,
				"ProductName":      brand.Name,
				"ProductVersion":   "0.1.0.0",
			}},
		}}},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err == nil {
		err = os.MkdirAll(out, 0o755)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(out, "winres.json"), b, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
