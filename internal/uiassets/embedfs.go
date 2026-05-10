package uiassets

import (
	"embed"
	"fmt"
	"io/fs"
)

// embeddedDist mirrors a built web UI so `godex serve` can fall back to a
// single-binary mode when ui/web/dist is unavailable on disk.
//
//go:embed embedded_dist/index.html embedded_dist/assets/* embedded_dist/brand/*
var embeddedDist embed.FS

// DistFS returns the embedded web UI filesystem rooted at the dist directory.
func DistFS() (fs.FS, error) {
	fsys, err := fs.Sub(embeddedDist, "embedded_dist")
	if err != nil {
		return nil, fmt.Errorf("load embedded web ui: %w", err)
	}
	return fsys, nil
}
