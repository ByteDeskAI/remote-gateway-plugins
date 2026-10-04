// Package msp is a client for the Firewalla MSP API v2
// (https://docs.firewalla.net/). Responses are passed through as raw JSON so
// the plugin never drops a field Firewalla adds.
package msp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ByteDeskAI/remote-gateway-plugins/kit"
)

// ErrNotConfigured is returned when the MSP domain or token is missing.
var ErrNotConfigured = errors.New("Firewalla MSP is not configured: set the MSP domain and MSP API token (FIREWALLA_MSP_DOMAIN, FIREWALLA_MSP_TOKEN)")

// Client calls https://<Domain>/v2 with "Authorization: Token <Token>".
type Client struct {
	Domain string
	Token  string
	// BaseURL overrides https://<Domain>/v2 (tests).
	BaseURL string
	HTTP    *http.Client
}

// Do sends one request and returns the response body. A non-2xx status is an
// error that carries Firewalla's message but never the token.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	if c == nil || strings.TrimSpace(c.Domain) == "" && c.BaseURL == "" || strings.TrimSpace(c.Token) == "" {
		return nil, ErrNotConfigured
	}
	base := c.BaseURL
	if base == "" {
		base = "https://" + strings.TrimSpace(c.Domain) + "/v2"
	}
	u := base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+strings.TrimSpace(c.Token))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("firewalla msp %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("firewalla msp %s %s: HTTP %d: %s", method, path, res.StatusCode, msg)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{"ok":true}`), nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("firewalla msp %s %s: response is not JSON", method, path)
	}
	return raw, nil
}

// MaxFlowLimit is the API's per-page ceiling for flows and alarms.
const MaxFlowLimit = 500

type (
	groupIn struct {
		Group string `json:"group" desc:"Box group ID (optional)"`
	}
	devicesIn struct {
		Box   string `json:"box" desc:"Box GID (optional)"`
		Group string `json:"group" desc:"Box group ID (optional)"`
	}
	renameIn struct {
		GID  string `json:"gid" kit:"required" desc:"Box GID"`
		ID   string `json:"id" kit:"required" desc:"Device ID (MAC address)"`
		Name string `json:"name" kit:"required" desc:"New name, at most 32 characters"`
	}
	queryIn struct {
		Query string `json:"query" desc:"Firewalla search query"`
	}
	ruleIDIn struct {
		ID string `json:"id" kit:"required" desc:"Rule ID"`
	}
	ruleCreateIn struct {
		Rule json.RawMessage `json:"rule" kit:"required" desc:"Rule object: action (block|allow), direction, gid, target, scope, notes"`
	}
	searchIn struct {
		Query    string `json:"query" desc:"Firewalla search query"`
		GroupBy  string `json:"groupBy" desc:"Comma-separated grouping, e.g. type,box"`
		SortBy   string `json:"sortBy" desc:"Comma-separated sort, e.g. ts:desc"`
		Limit    int    `json:"limit" desc:"Results per page, at most 500 (default 200)"`
		Cursor   string `json:"cursor" desc:"next_cursor from a previous page"`
		MaxPages int    `json:"maxPages" desc:"Follow next_cursor for up to this many pages (default 1, at most 20)"`
	}
	alarmIn struct {
		GID string      `json:"gid" kit:"required" desc:"Box GID"`
		AID json.Number `json:"aid" kit:"required" desc:"Alarm ID"`
	}
	muteIn struct {
		GID    string          `json:"gid" kit:"required" desc:"Box GID"`
		AID    json.Number     `json:"aid" kit:"required" desc:"Alarm ID"`
		Target json.RawMessage `json:"target" kit:"required" desc:"Mute target, e.g. {\"type\":\"domain\",\"value\":\"example.com\"}"`
		Scope  json.RawMessage `json:"scope" kit:"required" desc:"Mute scope, e.g. {\"type\":\"device\",\"value\":\"AA:BB:CC:DD:EE:FF\"}"`
	}
	statsIn struct {
		Type  string `json:"type" kit:"required" desc:"topBoxesByBlockedFlows, topBoxesBySecurityAlarms or topRegionsByBlockedFlows"`
		Group string `json:"group" desc:"Box group ID (optional)"`
		Limit int    `json:"limit" desc:"Max results (default 5)"`
	}
	listIDIn struct {
		ID string `json:"id" kit:"required" desc:"Target list ID"`
	}
	listCreateIn struct {
		List json.RawMessage `json:"list" kit:"required" desc:"Target list: name, targets[], owner (global or box GID), category, notes"`
	}
	listUpdateIn struct {
		ID   string          `json:"id" kit:"required" desc:"Target list ID"`
		List json.RawMessage `json:"list" kit:"required" desc:"Fields to change"`
	}
)

var statTypes = map[string]bool{"topBoxesByBlockedFlows": true, "topBoxesBySecurityAlarms": true, "topRegionsByBlockedFlows": true}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("invalid input: %w", err)
	}
	return v, nil
}

func need(pairs ...string) error {
	for i := 0; i < len(pairs); i += 2 {
		if strings.TrimSpace(pairs[i+1]) == "" {
			return fmt.Errorf("%s is required", pairs[i])
		}
	}
	return nil
}

func values(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	return v
}

func esc(s string) string { return url.PathEscape(strings.TrimSpace(s)) }

// search pages through a cursor-paged list endpoint (alarms, flows).
func search(ctx context.Context, c *Client, path string, in searchIn) (any, error) {
	if in.Limit < 0 || in.Limit > MaxFlowLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", MaxFlowLimit)
	}
	pages := in.MaxPages
	if pages <= 0 {
		pages = 1
	}
	if pages > 20 {
		return nil, errors.New("maxPages must be at most 20")
	}
	limit := ""
	if in.Limit > 0 {
		limit = fmt.Sprint(in.Limit)
	}
	var all []json.RawMessage
	cursor := in.Cursor
	for range pages {
		raw, err := c.Do(ctx, http.MethodGet, path, values("query", in.Query, "groupBy", in.GroupBy, "sortBy", in.SortBy, "limit", limit, "cursor", cursor), nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Results    []json.RawMessage `json:"results"`
			NextCursor *string           `json:"next_cursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("firewalla msp %s: unexpected response: %w", path, err)
		}
		all = append(all, page.Results...)
		cursor = ""
		if page.NextCursor != nil {
			cursor = *page.NextCursor
		}
		if cursor == "" {
			break
		}
	}
	if all == nil {
		all = []json.RawMessage{}
	}
	out := map[string]any{"count": len(all), "results": all, "next_cursor": nil}
	if cursor != "" {
		out["next_cursor"] = cursor
	}
	return out, nil
}

// Ops is the MSP half of the plugin's operation table. client is resolved on
// every call, so a settings change applies without a restart.
func Ops(client func() *Client) []kit.Op {
	op := func(name, summary string, risk kit.Risk, input any, h func(context.Context, *Client, json.RawMessage) (any, error)) kit.Op {
		return kit.Op{Name: name, Summary: summary, Risk: risk, Input: input, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return h(ctx, client(), raw)
		}}
	}
	return []kit.Op{
		op("boxes.list", "List Firewalla boxes in the MSP", kit.Read, groupIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[groupIn](raw)
			if err != nil {
				return nil, err
			}
			return c.Do(ctx, http.MethodGet, "/boxes", values("group", in.Group), nil)
		}),
		op("devices.list", "List devices, optionally for one box or box group", kit.Read, devicesIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[devicesIn](raw)
			if err != nil {
				return nil, err
			}
			return c.Do(ctx, http.MethodGet, "/devices", values("box", in.Box, "group", in.Group), nil)
		}),
		op("devices.rename", "Rename a device", kit.Write, renameIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[renameIn](raw)
			if err != nil {
				return nil, err
			}
			if err := need("gid", in.GID, "id", in.ID, "name", in.Name); err != nil {
				return nil, err
			}
			if len([]rune(in.Name)) > 32 {
				return nil, errors.New("name must be at most 32 characters")
			}
			return c.Do(ctx, http.MethodPatch, "/boxes/"+esc(in.GID)+"/devices/"+esc(in.ID), nil, map[string]string{"name": in.Name})
		}),
		op("rules.list", "List rules", kit.Read, queryIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[queryIn](raw)
			if err != nil {
				return nil, err
			}
			return c.Do(ctx, http.MethodGet, "/rules", values("query", in.Query), nil)
		}),
		op("rules.create", "Create a block or allow rule", kit.Write, ruleCreateIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[ruleCreateIn](raw)
			if err != nil {
				return nil, err
			}
			var rule struct {
				Action string `json:"action"`
			}
			if json.Unmarshal(in.Rule, &rule) != nil || (rule.Action != "block" && rule.Action != "allow") {
				return nil, errors.New("rule must be a JSON object whose action is block or allow")
			}
			return c.Do(ctx, http.MethodPost, "/rules", nil, in.Rule)
		}),
		op("rules.pause", "Pause a rule", kit.Write, ruleIDIn{}, ruleAction("pause")),
		op("rules.resume", "Resume a paused rule", kit.Write, ruleIDIn{}, ruleAction("resume")),
		op("rules.delete", "Delete a rule", kit.Destructive, ruleIDIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[ruleIDIn](raw)
			if err != nil {
				return nil, err
			}
			if err := need("id", in.ID); err != nil {
				return nil, err
			}
			return c.Do(ctx, http.MethodDelete, "/rules/"+esc(in.ID), nil, nil)
		}),
		op("alarms.list", "Search alarms (last 30 days unless the query has ts)", kit.Read, searchIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[searchIn](raw)
			if err != nil {
				return nil, err
			}
			return search(ctx, c, "/alarms", in)
		}),
		op("alarms.get", "Get one alarm", kit.Read, alarmIn{}, alarmAction(http.MethodGet, "")),
		op("alarms.archive", "Archive an alarm", kit.Write, alarmIn{}, alarmAction(http.MethodPost, "/archive")),
		op("alarms.mute", "Archive an alarm and silence matching future alarms", kit.Write, muteIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[muteIn](raw)
			if err != nil {
				return nil, err
			}
			if err := need("gid", in.GID, "aid", string(in.AID), "target", string(in.Target), "scope", string(in.Scope)); err != nil {
				return nil, err
			}
			return c.Do(ctx, http.MethodPost, "/alarms/"+esc(in.GID)+"/"+esc(string(in.AID))+"/mute", nil, map[string]json.RawMessage{"target": in.Target, "scope": in.Scope})
		}),
		op("alarms.delete", "Delete an alarm", kit.Destructive, alarmIn{}, alarmAction(http.MethodDelete, "")),
		op("flows.list", "Search flows (last 24 hours unless the query has ts), following cursors up to maxPages", kit.Read, searchIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[searchIn](raw)
			if err != nil {
				return nil, err
			}
			return search(ctx, c, "/flows", in)
		}),
		op("stats.simple", "Online/offline boxes, alarm and rule counts", kit.Read, groupIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[groupIn](raw)
			if err != nil {
				return nil, err
			}
			return c.Do(ctx, http.MethodGet, "/stats/simple", values("group", in.Group), nil)
		}),
		op("stats.top", "Top boxes or regions by blocked flows or security alarms", kit.Read, statsIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[statsIn](raw)
			if err != nil {
				return nil, err
			}
			if !statTypes[in.Type] {
				return nil, errors.New("type must be topBoxesByBlockedFlows, topBoxesBySecurityAlarms or topRegionsByBlockedFlows")
			}
			limit := ""
			if in.Limit > 0 {
				limit = fmt.Sprint(in.Limit)
			}
			return c.Do(ctx, http.MethodGet, "/stats/"+in.Type, values("group", in.Group, "limit", limit), nil)
		}),
		op("targetlists.list", "List target lists", kit.Read, nil, func(ctx context.Context, c *Client, _ json.RawMessage) (any, error) {
			return c.Do(ctx, http.MethodGet, "/target-lists", nil, nil)
		}),
		op("targetlists.get", "Get one target list", kit.Read, listIDIn{}, listAction(http.MethodGet)),
		op("targetlists.create", "Create a target list", kit.Write, listCreateIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[listCreateIn](raw)
			if err != nil {
				return nil, err
			}
			if err := need("list", string(in.List)); err != nil {
				return nil, err
			}
			return c.Do(ctx, http.MethodPost, "/target-lists", nil, in.List)
		}),
		op("targetlists.update", "Update a target list", kit.Write, listUpdateIn{}, func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
			in, err := decode[listUpdateIn](raw)
			if err != nil {
				return nil, err
			}
			if err := need("id", in.ID, "list", string(in.List)); err != nil {
				return nil, err
			}
			return c.Do(ctx, http.MethodPatch, "/target-lists/"+esc(in.ID), nil, in.List)
		}),
		op("targetlists.delete", "Delete a target list", kit.Destructive, listIDIn{}, listAction(http.MethodDelete)),
	}
}

func ruleAction(action string) func(context.Context, *Client, json.RawMessage) (any, error) {
	return func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
		in, err := decode[ruleIDIn](raw)
		if err != nil {
			return nil, err
		}
		if err := need("id", in.ID); err != nil {
			return nil, err
		}
		return c.Do(ctx, http.MethodPost, "/rules/"+esc(in.ID)+"/"+action, nil, nil)
	}
}

func alarmAction(method, suffix string) func(context.Context, *Client, json.RawMessage) (any, error) {
	return func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
		in, err := decode[alarmIn](raw)
		if err != nil {
			return nil, err
		}
		if err := need("gid", in.GID, "aid", string(in.AID)); err != nil {
			return nil, err
		}
		return c.Do(ctx, method, "/alarms/"+esc(in.GID)+"/"+esc(string(in.AID))+suffix, nil, nil)
	}
}

func listAction(method string) func(context.Context, *Client, json.RawMessage) (any, error) {
	return func(ctx context.Context, c *Client, raw json.RawMessage) (any, error) {
		in, err := decode[listIDIn](raw)
		if err != nil {
			return nil, err
		}
		if err := need("id", in.ID); err != nil {
			return nil, err
		}
		return c.Do(ctx, method, "/target-lists/"+esc(in.ID), nil, nil)
	}
}
