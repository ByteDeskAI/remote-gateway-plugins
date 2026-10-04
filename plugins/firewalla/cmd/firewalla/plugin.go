package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginsdk "github.com/ByteDeskAI/bytedesk-remote-gateway-plugin-sdk/v2"
	sdkhostsettings "github.com/ByteDeskAI/bytedesk-remote-gateway-plugin-sdk/v2/hostsettings"
	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/bus"
	"github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/hostsettings"
	commonplugin "github.com/ByteDeskAI/bytedesk-sdk-dependencies/v2/plugin"
	"github.com/ByteDeskAI/remote-gateway-plugins/kit"
	"github.com/ByteDeskAI/remote-gateway-plugins/plugins/firewalla"
	"github.com/ByteDeskAI/remote-gateway-plugins/plugins/firewalla/internal/box"
	"github.com/ByteDeskAI/remote-gateway-plugins/plugins/firewalla/internal/settingscontract"
	"golang.org/x/crypto/ssh"
)

const (
	pluginID  = "firewalla"
	sectionID = "firewalla"
	// The gateway's spawned-plugin MCP adapter discovers a service named after
	// the SDK point and calls its "tools" and "call" endpoints
	// (bytedesk-remote-gateway src/mcp_service_provider.go).
	mcpService = string(commonplugin.PointMCPTool)
	// tokenFile is the interim MSP token source in serve mode: the gateway
	// keeps secret settings host-private and offers plugins no way to read
	// them (see README "Known gaps").
	tokenFile = "msp-token"
)

type firewallaPlugin struct {
	pluginsdk.Base
	manifest pluginsdk.Manifest
	table    *kit.Table
	services []pluginsdk.Service

	mu   sync.Mutex
	last config
}

func serve(ctx context.Context) error {
	m, err := firewalla.Manifest()
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	return pluginsdk.ServePlugin(ctx, &firewallaPlugin{manifest: m}, pluginsdk.PluginConfig{})
}

func (p *firewallaPlugin) ID() string                   { return pluginID }
func (p *firewallaPlugin) Manifest() pluginsdk.Manifest { return p.manifest }
func (p *firewallaPlugin) Handler() http.Handler        { return p.table.Handler() }

func (p *firewallaPlugin) Start(ctx context.Context) error {
	state := p.StateDir()
	if state == "" {
		return errors.New("no plugin state directory")
	}
	signer, err := box.LoadOrCreateKey(state)
	if err != nil {
		return fmt.Errorf("ssh key: %w", err)
	}
	p.table = newTable(p.config,
		func() []ssh.AuthMethod { return []ssh.AuthMethod{ssh.PublicKeys(signer)} },
		filepath.Join(state, "known_hosts"),
		func() ssh.PublicKey { return signer.PublicKey() })

	mcp, err := p.Bus().Services().Serve(ctx, pluginsdk.ServiceSpec{
		Name: mcpService, Version: "1.0.0", QueueGroup: pluginID,
		Metadata: map[string]string{"provider": pluginID},
		Endpoints: []pluginsdk.EndpointSpec{
			{Name: "tools", Subject: "svc.firewalla.mcp.tool.v1.tools", Point: mcpService, Handler: func(_ context.Context, m *pluginsdk.Msg) {
				_ = m.Respond(nil, p.table.MCPToolsReply())
			}},
			{Name: "call", Subject: "svc.firewalla.mcp.tool.v1.call", Point: mcpService, Handler: func(ctx context.Context, m *pluginsdk.Msg) {
				_ = m.Respond(nil, p.table.MCPCall(ctx, m.Data))
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("serve MCP tools: %w", err)
	}
	p.services = append(p.services, mcp)

	validate, err := commonplugin.ServeAtPoint(ctx, p.Bus(), settingscontract.Validate, commonplugin.PointSettingsSection,
		func(_ context.Context, req hostsettings.ValidateRequest, _ bus.Caller) (hostsettings.ValidateResult, error) {
			return validateSettings(req), nil
		})
	if err != nil {
		_ = p.Stop(context.Background())
		return fmt.Errorf("serve settings validation: %w", err)
	}
	p.services = append(p.services, validate)
	return nil
}

func (p *firewallaPlugin) Stop(ctx context.Context) error {
	var errs []error
	for _, s := range p.services {
		errs = append(errs, s.Stop(ctx))
	}
	p.services = nil
	return errors.Join(errs...)
}

// config reads the host-owned settings on every call, keeping the last good
// values if the host does not answer. The token comes from the plugin
// environment or <state>/msp-token, never from the settings read.
func (p *firewallaPlugin) config() config {
	c := envConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := sdkhostsettings.Call(ctx, &p.Base, hostsettings.OwnerRead, hostsettings.OwnerReadRequest{SectionID: sectionID})
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		applySettings(&c, res.ValuesJSON)
		p.last = c
	} else if p.last != (config{}) {
		c = p.last
	}
	if c.MSPToken == "" {
		if raw, err := os.ReadFile(filepath.Join(p.StateDir(), tokenFile)); err == nil {
			c.MSPToken = strings.TrimSpace(string(raw))
		}
	}
	return c
}

func applySettings(c *config, valuesJSON string) {
	var v map[string]any
	if json.Unmarshal([]byte(valuesJSON), &v) != nil {
		return
	}
	str := func(k string) string { s, _ := v[k].(string); return strings.TrimSpace(s) }
	if s := str("mspDomain"); s != "" {
		c.MSPDomain = s
	}
	if s := str("boxHost"); s != "" {
		c.BoxHost = s
	}
	switch b := v["enableWrites"].(type) {
	case bool:
		c.Writes = b
	case string:
		c.Writes, _ = strconv.ParseBool(b)
	}
}

// validateSettings checks the merged, redacted values the host will commit.
func validateSettings(req hostsettings.ValidateRequest) hostsettings.ValidateResult {
	var v map[string]any
	if err := json.Unmarshal([]byte(req.ValuesJSON), &v); err != nil {
		return hostsettings.ValidateResult{Errors: []hostsettings.FieldError{{Field: sectionID, Message: "settings must be a JSON object"}}}
	}
	var errs []hostsettings.FieldError
	if d, _ := v["mspDomain"].(string); d != "" && (strings.ContainsAny(d, "/: ") || !strings.Contains(d, ".")) {
		errs = append(errs, hostsettings.FieldError{Field: "mspDomain", Message: "Enter the domain only, for example mycompany.firewalla.net"})
	}
	if h, _ := v["boxHost"].(string); h != "" {
		host := h
		if hh, _, err := net.SplitHostPort(h); err == nil {
			host = hh
		}
		if host == "" || strings.ContainsAny(host, "/ ") {
			errs = append(errs, hostsettings.FieldError{Field: "boxHost", Message: "Enter an IP address or host name, optionally with :port"})
		}
	}
	return hostsettings.ValidateResult{Valid: len(errs) == 0, Errors: errs}
}

var (
	_ pluginsdk.Plugin     = (*firewallaPlugin)(nil)
	_ pluginsdk.HTTPPlugin = (*firewallaPlugin)(nil)
)
