// Wormhole
// SPDX-License-Identifier: AGPL-3.0-or-later

package adminui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// FS returns the production admin application assets.
func FS() fs.FS {
	assets, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return assets
}
