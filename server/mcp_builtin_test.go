package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
)

func TestBuiltinMCPControls(t *testing.T) {
	if os.Getenv("ARTEX_TEST_MCP_CONTROLS") != "1" {
		t.Skip("requires an isolated PostgreSQL test database")
	}
	t.Setenv("ARTEX_LIGHTPANDA_MCP_URL", "http://127.0.0.1:9222/mcp")
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
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
	code, out := call(http.MethodGet, "/api/mcp", nil)
	if code != 200 {
		t.Fatalf("list MCP: %d %v", code, out)
	}
	var browserID float64
	var lightpanda map[string]any
	for _, raw := range out["servers"].([]any) {
		item := raw.(map[string]any)
		switch item["name"] {
		case "browser":
			if item["builtin"] != true {
				t.Fatal("browser must be built-in")
			}
			browserID = item["id"].(float64)
		case lightpandaReaderMCPName:
			lightpanda = item
		}
	}
	if browserID == 0 || lightpanda == nil || lightpanda["builtin"] != true || lightpanda["enabled"] != true {
		t.Fatalf("missing built-in MCPs: browser=%v Lightpanda=%v", browserID, lightpanda)
	}
	if lightpanda["display_name"] != "Lightpanda" {
		t.Fatalf("unexpected Lightpanda label: %v", lightpanda["display_name"])
	}
	if code, out := call(http.MethodPost, "/api/mcp", map[string]any{
		"id": -1, "name": lightpandaReaderMCPName, "display_name": "My Lightpanda",
		"transport": "stdio", "command": "lightpanda", "args": []string{"mcp"},
		"env": map[string]string{"TEST": "ok"}, "enabled": true,
	}); code != 200 {
		t.Fatalf("save Lightpanda stdio config: %d %v", code, out)
	}
	configured := lightpandaMCP(m.pg)
	if configured.DisplayName != "My Lightpanda" || configured.Transport != "stdio" || configured.Command != "lightpanda" || string(configured.Args) != `["mcp"]` {
		t.Fatalf("Lightpanda stdio config not retained: %+v", configured)
	}
	if code, out := call(http.MethodPost, "/api/mcp", map[string]any{
		"id": -1, "name": lightpandaReaderMCPName, "display_name": "Lightpanda",
		"transport": "http", "url": "http://127.0.0.1:9223/mcp",
		"env": map[string]string{"Host": "127.0.0.1"}, "enabled": true,
	}); code != 200 {
		t.Fatalf("save Lightpanda HTTP config: %d %v", code, out)
	}
	if configured = lightpandaMCP(m.pg); configured.URL != "http://127.0.0.1:9223/mcp" || configured.DisplayName != "Lightpanda" {
		t.Fatalf("Lightpanda HTTP config not retained: %+v", configured)
	}
	if code, _ := call(http.MethodPost, "/api/mcp", map[string]any{
		"id": -1, "name": lightpandaReaderMCPName, "display_name": "Bad",
		"transport": "http", "url": "file:///etc/passwd", "enabled": true,
	}); code != 400 || lightpandaMCP(m.pg).URL != configured.URL {
		t.Fatalf("invalid Lightpanda URL changed saved config: %d", code)
	}
	if err := m.pg.SetBool(settingBrowserScreenshot, true); err != nil {
		t.Fatal(err)
	}
	m.syncBrowserMCPProxy()
	if browser, err := m.pg.ListMCP(); err != nil {
		t.Fatal(err)
	} else {
		for _, item := range browser {
			if item.Name == browserMCPName && item.Enabled {
				t.Fatal("browser switch was overridden by screenshot setting")
			}
		}
	}
	for _, id := range []string{strconv.FormatInt(int64(browserID), 10), "-1"} {
		if code, _ := call(http.MethodDelete, "/api/mcp/"+id, nil); code != 400 {
			t.Fatalf("built-in MCP %s was deletable: %d", id, code)
		}
	}
	reporter, _ := m.pg.GetAgentByKey("reporter")
	mapper, _ := m.pg.GetAgentByKey("asset_mapper")
	worker, _ := m.pg.GetAgentByKey("worker")
	if reporter == nil || mapper == nil || worker == nil {
		t.Fatal("expected seeded agents")
	}
	if code, _ := call(http.MethodPost, "/api/mcp", map[string]any{"id": -1, "enabled": false}); code != 200 {
		t.Fatalf("disable Lightpanda: %d", code)
	}
	if code, out := call(http.MethodGet, "/api/agents/reporter", nil); code != 200 || out["runtime_prompt_append"] != "" {
		t.Fatalf("disabled Lightpanda left reporter prompt: %d %v", code, out["runtime_prompt_append"])
	}
	if code, _ := call(http.MethodPost, "/api/visibility/toggle", map[string]any{"agent_id": strconv.FormatInt(worker.ID, 10), "kind": "mcp", "resource_id": -1, "visible": true}); code != 400 {
		t.Fatalf("worker received Lightpanda: %d", code)
	}
	if code, _ := call(http.MethodPut, "/api/agents/worker/visibility", map[string]any{"mcp": []int64{-1}, "skill": []string{}}); code != 400 {
		t.Fatalf("worker received Lightpanda through agent editor: %d", code)
	}
	if code, _ := call(http.MethodPut, "/api/agents/asset_mapper/visibility", map[string]any{"mcp": []int64{-1}, "skill": []string{}}); code != 200 {
		t.Fatalf("asset mapper agent editor could not save Lightpanda: %d", code)
	}
	if code, _ := call(http.MethodPost, "/api/visibility/toggle", map[string]any{"agent_id": strconv.FormatInt(mapper.ID, 10), "kind": "mcp", "resource_id": -1, "visible": true}); code != 200 {
		t.Fatalf("grant asset mapper: %d", code)
	}
	if code, _ := call(http.MethodPost, "/api/visibility/toggle", map[string]any{"agent_id": strconv.FormatInt(reporter.ID, 10), "kind": "mcp", "resource_id": -1, "visible": false}); code != 200 {
		t.Fatalf("revoke reporter: %d", code)
	}
	if err := seedLightpandaVisibility(m.pg); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(http.MethodPost, "/api/mcp", map[string]any{"id": -1, "enabled": true}); code != 200 {
		t.Fatalf("enable Lightpanda: %d", code)
	}
	if code, out := call(http.MethodGet, "/api/agents/asset_mapper", nil); code != 200 || out["runtime_prompt_append"] == "" {
		t.Fatalf("asset mapper missing enabled prompt: %d", code)
	}
	if code, out := call(http.MethodGet, "/api/agents/reporter", nil); code != 200 || out["runtime_prompt_append"] != "" {
		t.Fatalf("revoked reporter retained prompt: %d", code)
	}
	var stored int
	if err := m.pg.QueryRow(`SELECT count(*) FROM mcp_servers WHERE name=$1`, lightpandaReaderMCPName).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("Lightpanda must not become a persisted MCP server: %d %v", stored, err)
	}
}
