package site

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

func Files() fs.FS {
	sub, _ := fs.Sub(assets, "dist")
	return sub
}
