package firewalla

import (
	"os"
	"strings"
	"testing"

	pluginsdk "github.com/ByteDeskAI/bytedesk-remote-gateway-plugin-sdk/v2"
)

func TestManifest(t *testing.T) {
	m, err := Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if err := pluginsdk.Validate(m); err != nil {
		t.Fatalf("SDK validate: %v", err)
	}
	version, _ := os.ReadFile("VERSION")
	if m.ID != "firewalla" || m.Version != strings.TrimSpace(string(version)) {
		t.Fatalf("id/version = %s/%s, VERSION = %q", m.ID, m.Version, version)
	}
}
