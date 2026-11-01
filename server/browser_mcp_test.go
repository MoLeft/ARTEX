package server

import (
	"encoding/json"
	"strings"
	"testing"
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
