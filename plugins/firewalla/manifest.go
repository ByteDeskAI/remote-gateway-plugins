// Package firewalla holds the plugin manifest. plugin.json is the single
// source: the binary embeds it, so the shipped file and the served manifest
// cannot drift.
package firewalla

import (
	_ "embed"
	"encoding/json"

	pluginsdk "github.com/ByteDeskAI/bytedesk-remote-gateway-plugin-sdk/v2"
)

//go:embed plugin.json
var ManifestJSON []byte

// Manifest decodes plugin.json.
func Manifest() (pluginsdk.Manifest, error) {
	var m pluginsdk.Manifest
	err := json.Unmarshal(ManifestJSON, &m)
	return m, err
}
