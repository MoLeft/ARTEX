package server

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func testBrowserMCP(t *testing.T, name string) *db.MCPServer {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	command := `while IFS= read -r line; do case "$line" in *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{}}';; *'"method":"tools/list"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"navigate","description":"Open a page","inputSchema":{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}},{"name":"browser_take_screenshot","description":"Take a screenshot","inputSchema":{"type":"object"}}]}}';; *'"method":"tools/call"'*) printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"visited"}]}}';; esac; done`
	return &db.MCPServer{Name: name, Transport: "stdio", Command: "sh", Args: encodeJSON([]string{"-c", command})}
}

func TestLazyBrowserToolsConnectOnlyWhenChosen(t *testing.T) {
	p := newBrowserPool(nil)
	p.setLimit(browserMCPKindChrome, 1)
	p.setLimit(browserMCPKindLightpanda, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	schema := json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)
	chrome, closeChrome := p.lazyTools(ctx, nil, testBrowserMCP(t, browserMCPName), "auto", agent.RunInfo{SessionID: "conv-chrome"}, []db.MCPTool{{Name: "mcp__browser__navigate", Description: "Open a page", Schema: schema}})
	defer closeChrome.Close()
	light, closeLight := p.lazyTools(ctx, nil, testBrowserMCP(t, lightpandaMCPName), "auto", agent.RunInfo{SessionID: "conv-light"}, []db.MCPTool{{Name: "mcp__lightpanda__navigate", Description: "Open a page", Schema: schema}})
	defer closeLight.Close()
	if len(chrome) != 1 || len(light) != 1 || p.status()[0].Active != 0 || p.status()[1].Active != 0 {
		t.Fatal("tool registration acquired a browser before the agent chose one")
	}
	if got := chrome[0].InputSchema()["required"].([]any)[0]; got != "url" {
		t.Fatalf("cached schema lost required URL: %v", got)
	}
	result, err := light[0].Call(ctx, json.RawMessage(`{"url":"https://example.test"}`), nil)
	if err != nil || result.IsError || !strings.Contains(result.Flatten(), "visited") {
		t.Fatalf("chosen browser call failed: %v %+v", err, result)
	}
	if p.status()[0].Active != 0 || p.status()[1].Active != 1 {
		t.Fatal("calling Lightpanda also acquired Chrome")
	}
	closeLight.Close()
	if p.status()[1].Active != 0 {
		t.Fatal("chosen browser slot stayed occupied after cleanup")
	}
}

func TestLazyBrowserToolWaitsOnlyAtFirstCall(t *testing.T) {
	p := newBrowserPool(nil)
	p.setLimit(browserMCPKindChrome, 1)
	blocker, err := p.acquire(context.Background(), browserMCPKindChrome, "busy", agent.RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	schema := json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)
	tools, cleanup := p.lazyTools(ctx, nil, testBrowserMCP(t, browserMCPName), "auto", agent.RunInfo{SessionID: "conv-wait"}, []db.MCPTool{{Name: "mcp__browser__navigate", Schema: schema}})
	defer cleanup.Close()
	if kind := p.waitingKind("conv-wait"); kind != "" {
		t.Fatalf("conversation queued before browser tool use: %s", kind)
	}
	result := make(chan bool, 1)
	go func() {
		res, err := tools[0].Call(ctx, json.RawMessage(`{"url":"https://example.test"}`), nil)
		result <- err == nil && !res.IsError
	}()
	deadline := time.Now().Add(time.Second)
	for p.waitingKind("conv-wait") != browserMCPKindChrome && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if kind := p.waitingKind("conv-wait"); kind != browserMCPKindChrome {
		t.Fatalf("first browser call did not enter Chrome queue: %q", kind)
	}
	blocker.Close()
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("browser call did not resume")
		}
	case <-ctx.Done():
		t.Fatal("browser call stayed queued after slot release")
	}
}

func TestLazyBrowserFallbackCanDiscoverToolsOnDemand(t *testing.T) {
	p := newBrowserPool(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tools, cleanup := p.lazyTools(ctx, nil, testBrowserMCP(t, browserMCPName), "auto", agent.RunInfo{SessionID: "conv-new"}, []db.MCPTool{{Name: "mcp__browser__navigate"}})
	defer cleanup.Close()
	if len(tools) != 1 || p.status()[0].Active != 0 {
		t.Fatal("missing catalog blocked chat or acquired Chrome")
	}
	listed, err := tools[0].Call(ctx, json.RawMessage(`{"tool":"__list_tools__"}`), nil)
	if err != nil || listed.IsError || !strings.Contains(listed.Flatten(), `"mcp__browser__navigate"`) {
		t.Fatalf("on-demand tool discovery failed: %v %+v", err, listed)
	}
	called, err := tools[0].Call(ctx, json.RawMessage(`{"tool":"navigate","arguments":{"url":"https://example.test"}}`), nil)
	if err != nil || called.IsError || !strings.Contains(called.Flatten(), "visited") {
		t.Fatalf("on-demand browser call failed: %v %+v", err, called)
	}
}

func TestLazyBrowserFallbackKeepsScreenshotGate(t *testing.T) {
	p := newBrowserPool(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tools, cleanup := p.lazyTools(ctx, nil, testBrowserMCP(t, browserMCPName), "auto", agent.RunInfo{SessionID: "conv-no-screenshot"}, nil)
	defer cleanup.Close()
	result, err := tools[0].Call(ctx, json.RawMessage(`{"tool":"browser_take_screenshot"}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Flatten(), "未启用") || p.status()[0].Active != 0 {
		t.Fatalf("screenshot fallback bypassed gate or acquired Chrome: %v %+v", err, result)
	}
	listed, err := tools[0].Call(ctx, json.RawMessage(`{"tool":"__list_tools__"}`), nil)
	if err != nil || listed.IsError || strings.Contains(listed.Flatten(), "screenshot") {
		t.Fatalf("screenshot tool leaked through fallback catalog: %v %+v", err, listed)
	}
}

func TestLazyBrowserRejectsStaleCachedSchema(t *testing.T) {
	p := newBrowserPool(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stale := json.RawMessage(`{"type":"object","properties":{"script":{"type":"string"}}}`)
	tools, cleanup := p.lazyTools(ctx, nil, testBrowserMCP(t, browserMCPName), "auto", agent.RunInfo{SessionID: "conv-stale"}, []db.MCPTool{{Name: "mcp__browser__navigate", Schema: stale}})
	defer cleanup.Close()
	result, err := tools[0].Call(ctx, json.RawMessage(`{"script":"unexpected"}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Flatten(), "参数格式已更新") {
		t.Fatalf("stale cached schema allowed a browser call: %v %+v", err, result)
	}
}
