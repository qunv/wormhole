// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package assets

import (
	"embed"
	"io/fs"
)

//go:embed data/*
var files embed.FS

func Widget() []byte {
	raw, _ := files.ReadFile("data/lca-compact-input-v2.html")
	return raw
}

func Skills() (map[string]string, error) {
	entries, err := fs.ReadDir(files, "data")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) < 7 || name[len(name)-6:] != ".skill" {
			continue
		}
		raw, err := files.ReadFile("data/" + name)
		if err != nil {
			return nil, err
		}
		out[name[:len(name)-6]] = string(raw)
	}
	return out, nil
}
