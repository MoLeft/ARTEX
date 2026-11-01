package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestBrowserMCPControls covers the optional browser MCPs end to end: the
// settings switches create ordinary MCP rows, the MCP API can delete them, and
// both views stay in sync because they read the same table.
func TestBrowserMCPControls(t *testing.T) {
	if os.Getenv("ARTEX_TEST_MCP_CONTROLS") != "1" {
		t.Skip("requires an isolated PostgreSQL test database")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("database unavailable (%v)", err)
	}
	if m.pg == nil {
		t.Skip("postgres unavailable")
	}
	defer m.Close()
	// Keep reruns idempotent.
	m.pg.Exec(`DELETE FROM mcp_servers WHERE name IN ('browser','lightpanda')`)
	m.pg.Exec(`DELETE FROM settings WHERE key IN ('browser_mcp_priority','browser_screenshot_enabled')`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(payload))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		var result map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return w.Code, result
	}
	mcpByName := func(name string) map[string]any {
		t.Helper()
		code, out := call(http.MethodGet, "/api/mcp", nil)
		if code != 200 {
			t.Fatalf("list MCP: %d %v", code, out)
		}
		for _, raw := range out["servers"].([]any) {
			item := raw.(map[string]any)
			if item["name"] == name {
				return item
			}
		}
		return nil
	}
	stateFor := func(out map[string]any, kind string) map[string]any {
		t.Helper()
		for _, raw := range out["servers"].([]any) {
			item := raw.(map[string]any)
			if item["kind"] == kind {
				return item
			}
		}
		t.Fatalf("kind %s missing from browser MCP state: %v", kind, out)
		return nil
	}

	// Nothing is seeded any more: the two browser MCPs are opt-in.
	if got := mcpByName(browserMCPName); got != nil {
		t.Fatalf("browser MCP must not be auto-created: %v", got)
	}
	if got := mcpByName(lightpandaMCPName); got != nil {
		t.Fatalf("Lightpanda MCP must not be auto-created: %v", got)
	}

	// Turning Chrome MCP on creates a plain, enabled row.
	code, out := call(http.MethodPost, "/api/settings/browser-mcp", map[string]any{"kind": "chrome", "enabled": true})
	if code != 200 {
		t.Fatalf("enable chrome: %d %v", code, out)
	}
	chromeState := stateFor(out, browserMCPKindChrome)
	if chromeState["created"] != true || chromeState["enabled"] != true {
		t.Fatalf("chrome state after enable: %v", chromeState)
	}
	chrome := mcpByName(browserMCPName)
	if chrome == nil || chrome["enabled"] != true || chrome["transport"] != "stdio" {
		t.Fatalf("chrome row: %v", chrome)
	}
	if _, ok := chrome["builtin"]; ok {
		t.Fatalf("browser MCP must not be flagged built-in any more: %v", chrome)
	}
	args := decodeStrSlice(mustJSON(chrome["args"]))
	if len(args) == 0 || args[0] != "@playwright/mcp" {
		t.Fatalf("chrome args unreadable: %v", args)
	}
	chromeID := int64(chrome["id"].(float64))

	// The MCP page owns the same row: disabling there is reflected in settings,
	// and deletion is allowed (no more protected built-in).
	postRow := func(row map[string]any, enabled bool) (int, map[string]any) {
		body := map[string]any{}
		for k, v := range row {
			body[k] = v
		}
		body["enabled"] = enabled
		return call(http.MethodPost, "/api/mcp", body)
	}
	if code, out := postRow(chrome, false); code != 200 {
		t.Fatalf("disable chrome from MCP page: %d %v", code, out)
	}
	_, out = call(http.MethodPost, "/api/settings/browser-mcp", map[string]any{"kind": "chrome", "enabled": true})
	if st := stateFor(out, browserMCPKindChrome); st["enabled"] != true {
		t.Fatalf("settings switch did not re-enable: %v", st)
	}
	if code, out := postRow(mcpByName(browserMCPName), false); code != 200 {
		t.Fatalf("disable chrome: %d %v", code, out)
	}
	_, out = call(http.MethodPost, "/api/settings/browser-mcp", map[string]any{"kind": "lightpanda", "enabled": true})
	if st := stateFor(out, browserMCPKindChrome); st["created"] != true || st["enabled"] != false {
		t.Fatalf("chrome state should mirror the MCP row: %v", st)
	}

	// Lightpanda is created as a plain http MCP pointing at the bundled process.
	lightpanda := mcpByName(lightpandaMCPName)
	if lightpanda == nil || lightpanda["transport"] != "http" || lightpanda["enabled"] != true {
		t.Fatalf("lightpanda row: %v", lightpanda)
	}
	if url, _ := lightpanda["url"].(string); url == "" {
		t.Fatalf("lightpanda row needs a URL: %v", lightpanda)
	}

	// Report screenshots still gate the browser MCP's reporter permission.
	if code, out := call(http.MethodPut, "/api/settings", map[string]any{"browser_screenshot_enabled": true}); code != 200 {
		t.Fatalf("enable screenshots: %d %v", code, out)
	}
	reporter, _ := m.pg.GetAgentByKey("reporter")
	if reporter == nil {
		t.Fatal("expected a seeded reporter agent")
	}
	visible, _ := m.pg.AgentVisible(reporter.ID, "mcp")
	foundReporterGrant := false
	for _, id := range visible {
		foundReporterGrant = foundReporterGrant || id == chromeID
	}
	if !foundReporterGrant {
		t.Fatalf("screenshot opt-in did not grant reporter the browser MCP: %v", visible)
	}
	if code, _ := call(http.MethodPut, "/api/settings", map[string]any{"browser_screenshot_enabled": false}); code != 200 {
		t.Fatalf("disable screenshots: %d", code)
	}
	visible, _ = m.pg.AgentVisible(reporter.ID, "mcp")
	for _, id := range visible {
		if id == chromeID {
			t.Fatal("screenshot opt-out kept reporter visibility")
		}
	}

	// The experimental priority only becomes observable when both browser MCPs
	// are visible to one agent: the runtime hint then names the preferred one.
	if code, out := call(http.MethodPost, "/api/settings/browser-mcp", map[string]any{"kind": "chrome", "enabled": true}); code != 200 {
		t.Fatalf("re-enable chrome: %d %v", code, out)
	}
	lightID := int64(lightpanda["id"].(float64))
	for _, id := range []int64{chromeID, lightID} {
		if code, out := call(http.MethodPost, "/api/visibility/toggle", map[string]any{
			"agent_id": strconv.FormatInt(reporter.ID, 10), "kind": "mcp", "resource_id": id, "visible": true,
		}); code != 200 {
			t.Fatalf("grant reporter MCP %d: %d %v", id, code, out)
		}
	}
	runtimePrompt := func() string {
		t.Helper()
		code, out := call(http.MethodGet, "/api/agents/reporter", nil)
		if code != 200 {
			t.Fatalf("get reporter: %d %v", code, out)
		}
		text, _ := out["runtime_prompt_append"].(string)
		return text
	}
	chromeHint := runtimePrompt()
	if !strings.Contains(chromeHint, "浏览器 MCP 优先级") || !strings.Contains(chromeHint, "优先使用 Chrome") {
		t.Fatalf("default priority hint missing: %q", chromeHint)
	}
	if code, out := call(http.MethodPut, "/api/settings", map[string]any{"browser_mcp_priority": "lightpanda"}); code != 200 {
		t.Fatalf("switch priority: %d %v", code, out)
	}
	if hint := runtimePrompt(); !strings.Contains(hint, "优先使用 Lightpanda") {
		t.Fatalf("lightpanda priority hint missing: %q", hint)
	}
	for _, id := range []int64{chromeID, lightID} {
		if code, _ := call(http.MethodPost, "/api/visibility/toggle", map[string]any{
			"agent_id": strconv.FormatInt(reporter.ID, 10), "kind": "mcp", "resource_id": id, "visible": false,
		}); code != 200 {
			t.Fatalf("revoke reporter MCP %d: %d", id, code)
		}
	}
	if hint := runtimePrompt(); hint != "" {
		t.Fatalf("no browser MCP visible, prompt should be empty: %q", hint)
	}

	// Both switches read the same table, so a delete clears the settings state.
	if code, _ := call(http.MethodDelete, "/api/mcp/"+strconv.FormatInt(chromeID, 10), nil); code != 200 {
		t.Fatalf("browser MCP must be deletable, got %d", code)
	}
	_, out = call(http.MethodPost, "/api/settings/browser-mcp", map[string]any{"kind": "lightpanda", "enabled": true})
	if st := stateFor(out, browserMCPKindChrome); st["created"] != false || st["enabled"] != false {
		t.Fatalf("deleted chrome still reported as available: %v", st)
	}
	if code, _ := call(http.MethodDelete, "/api/mcp/"+strconv.FormatInt(lightID, 10), nil); code != 200 {
		t.Fatalf("Lightpanda MCP must be deletable, got %d", code)
	}

	// Experimental priority setting: validated enum, default chrome.
	if code, out := call(http.MethodGet, "/api/settings", nil); code != 200 || out["browser_mcp_priority"] != browserMCPKindLightpanda {
		t.Fatalf("saved priority not read back: %d %v", code, out["browser_mcp_priority"])
	}
	if code, out := call(http.MethodPut, "/api/settings", map[string]any{"browser_mcp_priority": "chrome"}); code != 200 || out["browser_mcp_priority"] != browserMCPKindChrome {
		t.Fatalf("restore priority: %d %v", code, out["browser_mcp_priority"])
	}
	if code, _ := call(http.MethodPut, "/api/settings", map[string]any{"browser_mcp_priority": "firefox"}); code != 400 {
		t.Fatalf("invalid priority should be rejected, got %d", code)
	}
	if code, _ := call(http.MethodPost, "/api/settings/browser-mcp", map[string]any{"kind": "firefox", "enabled": true}); code != 400 {
		t.Fatalf("unknown browser MCP kind should be rejected, got %d", code)
	}

	// Cleanup so the control test leaves the database as it found it.
	m.pg.Exec(`DELETE FROM settings WHERE key IN ('browser_mcp_priority','browser_screenshot_enabled')`)
}
