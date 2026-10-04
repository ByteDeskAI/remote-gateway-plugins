package main

import (
	"context"
	"testing"
	"time"

	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/bus"
	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/bus/memory"
	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/plugin"
)

// The embedded plugin.json must be a manifest the host will admit.
func TestManifestValidates(t *testing.T) {
	m := New().Manifest()
	if m.ID != ID {
		t.Fatalf("plugin.json id = %q, want %q", m.ID, ID)
	}
	if err := plugin.ValidateDiscover(m); err != nil {
		t.Fatalf("manifest does not validate: %v", err)
	}
}

// Start serves ping over an in-memory bus; Stop releases it.
func TestPing(t *testing.T) {
	grants := plugin.OwnNamespace(ID)
	// The test plays another caller, which needs a request grant this plugin
	// never gets for its own namespace.
	grants.Request = []bus.Pattern{"svc.example.v1.ping"}
	id := bus.Identity{PluginID: ID, Generation: "test", Grants: grants}
	b := memory.NewStore().Connect(id)

	p := New()
	if err := plugin.Bind(p, plugin.Binding{Bus: b, Identity: id}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	got, err := plugin.Call(ctx, b, pingCommand, pingRequest{})
	if err != nil || !got.Pong {
		t.Fatalf("ping = %+v, %v", got, err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
