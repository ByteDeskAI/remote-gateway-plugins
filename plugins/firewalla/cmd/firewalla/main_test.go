package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pluginsdk "github.com/ByteDeskAI/bytedesk-remote-gateway-plugin-sdk/v2"
	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/bus"
	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/bus/memory"
	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/hostsettings"
	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/plugin"
	"github.com/ByteDeskAI/remote-gateway-plugins/plugins/firewalla"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	for _, k := range []string{"FIREWALLA_MSP_DOMAIN", "FIREWALLA_MSP_TOKEN", "FIREWALLA_BOX_HOST", "FIREWALLA_ENABLE_WRITES", "SSH_AUTH_SOCK"} {
		t.Setenv(k, "")
	}
	t.Setenv("FIREWALLA_STATE_DIR", t.TempDir())
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCLIWithoutConfig(t *testing.T) {
	if code, out, _ := runCLI(t, "--help"); code != 0 || !strings.Contains(out, "boxes list") || !strings.Contains(out, "FIREWALLA_MSP_TOKEN") {
		t.Fatalf("--help = %d %q", code, out)
	}
	if code, _, errOut := runCLI(t, "boxes", "list"); code != 1 || !strings.Contains(errOut, "FIREWALLA_MSP_DOMAIN") {
		t.Fatalf("boxes list = %d %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, "box", "interfaces"); code != 1 || !strings.Contains(errOut, "FIREWALLA_BOX_HOST") {
		t.Fatalf("box interfaces = %d %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, "rules", "delete", "--id=r1", "--confirm"); code != 1 || !strings.Contains(errOut, "disabled") {
		t.Fatalf("write with writes off = %d %q", code, errOut)
	}
	if code, _, _ := runCLI(t, "ssh", "bootstrap"); code != 2 {
		t.Fatalf("bootstrap without flags = %d", code)
	}
}

func TestValidateSettings(t *testing.T) {
	ok := validateSettings(hostsettings.ValidateRequest{SectionID: "firewalla", ValuesJSON: `{"mspDomain":"acme.firewalla.net","boxHost":"192.168.1.1","enableWrites":false}`})
	if !ok.Valid || ok.Validate() != nil {
		t.Fatalf("valid settings = %+v", ok)
	}
	bad := validateSettings(hostsettings.ValidateRequest{SectionID: "firewalla", ValuesJSON: `{"mspDomain":"https://acme.firewalla.net/","boxHost":"a b"}`})
	if bad.Valid || len(bad.Errors) != 2 || bad.Validate() != nil {
		t.Fatalf("invalid settings = %+v", bad)
	}
	var c config
	applySettings(&c, `{"mspDomain":"d.firewalla.net","boxHost":"10.0.0.1","enableWrites":true}`)
	if c != (config{MSPDomain: "d.firewalla.net", BoxHost: "10.0.0.1", Writes: true}) {
		t.Fatalf("applied = %+v", c)
	}
}

// TestServeOverBus runs the real plugin lifecycle on the SDK's in-memory bus
// and calls the MCP endpoints the way the gateway's adapter does.
func TestServeOverBus(t *testing.T) {
	store := memory.NewStore(memory.WithCapabilities(bus.Capabilities{Services: true, MaxPayload: 64 << 10}))
	t.Cleanup(store.Close)
	grants := plugin.OwnNamespace(pluginID)
	grants.Subscribe = append(grants.Subscribe, "cmd.plugin.v1.>")
	grants.Request = append(grants.Request, "cmd.plugin.v1.>", bus.Pattern("svc."+pluginID+".>"))
	mem := store.Connect(bus.Identity{PluginID: pluginID, Generation: "gen-1", Role: bus.RoleHost, Grants: grants})
	t.Cleanup(func() { _ = mem.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := t.TempDir()
	neg, err := mem.Subscribe(ctx, pluginsdk.Pattern(pluginsdk.NegotiateSubject), func(_ context.Context, m *bus.Msg) {
		raw, _ := json.Marshal(pluginsdk.HostCapabilities{
			Major: pluginsdk.ProtocolMajor, PluginID: pluginID, Generation: "gen-1",
			Features: []string{pluginsdk.FeatureLifecycleEndpoints, pluginsdk.FeatureHTTPRoutes, pluginsdk.FeatureGrantsDigest},
			Bus:      bus.Capabilities{Services: true, MaxPayload: 64 << 10},
			Identity: bus.Identity{PluginID: pluginID, Generation: "gen-1", Role: bus.RolePlugin},
			StateDir: state,
		})
		_ = m.Respond(nil, raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer neg.Cancel()

	m, err := firewalla.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	p := &firewallaPlugin{manifest: m}
	done := make(chan error, 1)
	go func() {
		done <- pluginsdk.ServePlugin(ctx, p, pluginsdk.PluginConfig{Socket: filepath.Join(t.TempDir(), "plugin.sock"), Bus: mem})
	}()

	var tools []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if reply, err := mem.Request(ctx, "svc.firewalla.mcp.tool.v1.tools", nil); err == nil {
			tools = reply.Data
			break
		}
	}
	if !strings.Contains(string(tools), `"firewalla_boxes_list"`) || !strings.Contains(string(tools), `"firewalla_box_leases"`) {
		t.Fatalf("tools = %s", tools)
	}
	found, err := mem.Services().Discover(ctx, string(plugin.PointMCPTool))
	if err != nil || len(found) != 1 || found[0].Metadata["provider"] != pluginID || len(found[0].Endpoints) != 2 {
		t.Fatalf("discover = %+v, %v", found, err)
	}
	reply, err := mem.Request(ctx, "svc.firewalla.mcp.tool.v1.call", []byte(`{"name":"firewalla_rules_delete","arguments":{"id":"r1","confirm":true}}`))
	if err != nil || !strings.Contains(string(reply.Data), "disabled") {
		t.Fatalf("write call = %s, %v", reply.Data, err)
	}
	if fi, err := os.Stat(filepath.Join(state, "id_ed25519")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("plugin key = %v, %v", fi, err)
	}
	cancel()
	<-done
}
