// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package assets

import "embed"

//go:embed data/*
var files embed.FS

func Widget() []byte {
	raw, _ := files.ReadFile("data/cb-compact-input-v2.html")
	return raw
}
