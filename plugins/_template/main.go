// Command example is a minimal contract-2 process plugin for the ByteDesk
// remote gateway, over the v2 (NATS transport) SDK. It serves one command,
// svc.example.v1.ping, which answers {"pong": true}.
//
// plugin.json is the only manifest: it is embedded here, so the manifest the
// gateway reads from disk and the one this process reports cannot drift.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"

	pluginsdk "github.com/ByteDeskAI/bytedesk-remote-gateway-plugin-sdk/v2"
	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/bus"
	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/plugin"
)

// ID is this plugin's identity. Everything it serves sits under svc.<ID>.
const ID = "example"

//go:embed plugin.json
var manifestJSON []byte

// Pong is the reply to a ping.
type Pong struct {
	Pong bool `json:"pong"`
}

type pingRequest struct{}

// The schema hash is a fixed label, not a computed hash: this plugin is the
// only sender and receiver. Generate descriptors with contractgen once a plugin
// has more than a couple of operations.
var pingCommand = plugin.NewCommand[pingRequest, Pong]("svc.example.v1.ping", 1, "example-ping-v1", "svc.example.v1.ping")

// Plugin embeds Base, which supplies the bus and logger once the host binds it.
type Plugin struct {
	plugin.Base

	svc bus.Service
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string { return ID }

func (p *Plugin) Manifest() pluginsdk.Manifest {
	var m pluginsdk.Manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		panic("embedded plugin.json: " + err.Error()) // caught by the tests
	}
	return m
}

func (p *Plugin) Start(ctx context.Context) error {
	svc, err := plugin.Serve(ctx, p.Bus(), pingCommand, func(context.Context, pingRequest, bus.Caller) (Pong, error) {
		return Pong{Pong: true}, nil
	})
	if err != nil {
		return fmt.Errorf("serve ping: %w", err)
	}
	p.svc = svc
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error {
	if p.svc != nil {
		return p.svc.Stop(ctx)
	}
	return nil
}

var _ pluginsdk.Plugin = (*Plugin)(nil)

func main() {
	if err := pluginsdk.ServePlugin(context.Background(), New(), pluginsdk.PluginConfig{}); err != nil {
		log.Fatal(err)
	}
}
