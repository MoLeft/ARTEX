package server

import (
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/traffic"
)

func TestNormalizeBrowserMCPKind(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"chrome", browserMCPKindChrome},
		{" Chrome ", browserMCPKindChrome},
		{"lightpanda", browserMCPKindLightpanda},
		{"LIGHTPANDA", browserMCPKindLightpanda},
	} {
		got, err := normalizeBrowserMCPKind(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("normalize(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "firefox", "browser"} {
		if _, err := normalizeBrowserMCPKind(in); err == nil {
			t.Fatalf("normalize(%q) should fail", in)
		}
	}
}

func TestBrowserMCPNameForKind(t *testing.T) {
	if got := browserMCPNameFor(browserMCPKindChrome); got != browserMCPName {
		t.Fatalf("chrome kind maps to %q, want %q", got, browserMCPName)
	}
	if got := browserMCPNameFor(browserMCPKindLightpanda); got != lightpandaMCPName {
		t.Fatalf("lightpanda kind maps to %q, want %q", got, lightpandaMCPName)
	}
}

func TestLightpandaMCPURLUsesEnvOrDefault(t *testing.T) {
	t.Setenv("ARTEX_LIGHTPANDA_MCP_URL", "")
	if got := lightpandaMCPURL(); got != defaultLightpandaURL {
		t.Fatalf("empty env → %q, want %q", got, defaultLightpandaURL)
	}
	t.Setenv("ARTEX_LIGHTPANDA_MCP_URL", "http://10.0.0.5:9222/mcp")
	if got := lightpandaMCPURL(); got != "http://10.0.0.5:9222/mcp" {
		t.Fatalf("valid env ignored: %q", got)
	}
	// A broken override must not be written into an MCP row.
	t.Setenv("ARTEX_LIGHTPANDA_MCP_URL", "file:///etc/passwd")
	if got := lightpandaMCPURL(); got != defaultLightpandaURL {
		t.Fatalf("invalid env → %q, want fallback %q", got, defaultLightpandaURL)
	}
}

func TestDefaultBrowserMCPConfigs(t *testing.T) {
	chrome := defaultBrowserMCP(browserMCPKindChrome)
	if chrome.Name != browserMCPName || chrome.Transport != "stdio" || chrome.Command != "npx" || !chrome.Enabled {
		t.Fatalf("unexpected chrome defaults: %+v", chrome)
	}
	if args := decodeStrSlice(chrome.Args); len(args) == 0 || args[0] != "@playwright/mcp" {
		t.Fatalf("chrome args: %v", args)
	}

	t.Setenv("ARTEX_LIGHTPANDA_MCP_URL", "")
	light := defaultBrowserMCP(browserMCPKindLightpanda)
	if light.Name != lightpandaMCPName || light.Transport != "http" || light.URL != defaultLightpandaURL || !light.Enabled {
		t.Fatalf("unexpected lightpanda defaults: %+v", light)
	}
	var headers map[string]string
	if err := json.Unmarshal(light.Env, &headers); err != nil || headers["Host"] == "" {
		t.Fatalf("lightpanda headers: %s (%v)", light.Env, err)
	}
}

func TestBrowserMCPUsesEffectiveProxyInBothCaptureModes(t *testing.T) {
	m := proxyTestManager(t)
	port, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := port.Addr().String()
	_ = port.Close()
	m.traffic, err = traffic.Open(t.TempDir(), addr)
	if err != nil {
		t.Fatal(err)
	}
	servers, err := m.pg.ListMCP()
	if err != nil {
		t.Fatal(err)
	}
	var browser *db.MCPServer
	for _, row := range servers {
		if row.Name == browserMCPName {
			browser = row
			break
		}
	}
	if browser == nil {
		browser = defaultBrowserMCP(browserMCPKindChrome)
		id, err := m.pg.SaveMCP(browser)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.pg.DeleteMCP(id) })
	} else {
		original := *browser
		t.Cleanup(func() { _, _ = m.pg.SaveMCP(&original) })
	}
	m.globalProxy = "http://127.0.0.1:18080"
	m.syncBrowserMCPProxy()
	check := func(wantProxy, wantCA string) {
		t.Helper()
		rows, err := m.pg.ListMCP()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Name != browserMCPName {
				continue
			}
			args := strings.Join(decodeStrSlice(row.Args), " ")
			if !strings.Contains(args, "--proxy-server "+wantProxy) || decodeStrMap(row.Env)["NODE_EXTRA_CA_CERTS"] != wantCA {
				t.Fatalf("args=%q env=%s, want proxy=%q CA=%q", args, row.Env, wantProxy, wantCA)
			}
			return
		}
		t.Fatal("browser MCP row vanished")
	}
	check(m.egress.Addr(), "")
	m.trafficOn = true
	m.syncBrowserMCPProxy()
	check(m.traffic.ProxyAddr(), m.traffic.CACertPath())
}

func TestEnsureBrowserMCPArgsKeepsContainerFlags(t *testing.T) {
	args := ensureBrowserMCPArgs([]string{"@playwright/mcp"}, false)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--no-sandbox") || !strings.Contains(joined, "--headless") {
		t.Fatalf("container flags missing: %v", args)
	}
	if strings.Contains(joined, "--isolated") {
		t.Fatalf("screenshot-only flag leaked: %v", args)
	}

	shot := ensureBrowserMCPArgs([]string{"@playwright/mcp", "--user-data-dir=/tmp/profile"}, true)
	joined = strings.Join(shot, " ")
	if !strings.Contains(joined, "--isolated") {
		t.Fatalf("isolated profile missing with screenshots on: %v", shot)
	}
	if strings.Contains(joined, "--user-data-dir") {
		t.Fatalf("persistent profile must be dropped for isolated mode: %v", shot)
	}
}
