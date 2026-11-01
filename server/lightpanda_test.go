package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestLightpandaReadToolBoundary(t *testing.T) {
	for _, name := range []string{
		"mcp__artex_lightpanda_reader__goto",
		"mcp__artex_lightpanda_reader__markdown",
		"mcp__artex_lightpanda_reader__html",
		"mcp__artex_lightpanda_reader__tree",
		"mcp__artex_lightpanda_reader__links",
		"mcp__artex_lightpanda_reader__getUrl",
		"mcp__artex_lightpanda_reader__waitForSelector",
		"mcp__artex_lightpanda_reader__waitForState",
	} {
		if !isLightpandaReadTool(name) {
			t.Errorf("read tool %s was rejected", name)
		}
	}
	for _, name := range []string{
		"mcp__artex_lightpanda_reader__screenshot",
		"mcp__artex_lightpanda_reader__click",
		"mcp__artex_lightpanda_reader__fill",
		"mcp__artex_lightpanda_reader__evaluate",
		"mcp__artex_lightpanda_reader__getCookies",
		"mcp__other__goto",
	} {
		if isLightpandaReadTool(name) {
			t.Errorf("non-read tool %s was allowed", name)
		}
	}
}

func TestLightpandaRuntimeMCPFollowsVersionState(t *testing.T) {
	t.Setenv("ARTEX_LIGHTPANDA_MCP_URL", "")
	if lightpandaMCP(nil) != nil {
		t.Fatal("Lightpanda should not be listed without a running MCP")
	}
	t.Setenv("ARTEX_LIGHTPANDA_MCP_URL", "http://127.0.0.1:9222/mcp")
	m := lightpandaMCP(nil)
	if m == nil || m.ID != lightpandaReaderMCPID || !m.Builtin || !m.Enabled || len(m.Tools) != 8 {
		t.Fatalf("invalid runtime MCP: %+v", m)
	}
	for _, name := range m.Tools {
		if !isLightpandaReadTool(name) {
			t.Errorf("advertised tool %q is outside the allowed set", name)
		}
	}
	if len(m.AllowedAgents) != 2 || !lightpandaAgentAllowed("reporter") || !lightpandaAgentAllowed("asset_mapper") || lightpandaAgentAllowed("worker") || lightpandaAgentAllowed("pentest") {
		t.Fatalf("invalid agent scope: %v", m.AllowedAgents)
	}
}

func TestLightpandaOnlyExposesReadToolsToReporter(t *testing.T) {
	url := os.Getenv("ARTEX_TEST_LIGHTPANDA_URL")
	if url == "" {
		t.Skip("requires a local Lightpanda MCP fixture")
	}
	dsn, _, err := db.DSN()
	if err != nil {
		t.Fatal(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if existing, err := pg.GetAgentByKey("reporter"); err != nil {
		t.Fatal(err)
	} else if existing == nil {
		if _, err := pg.CreateAgent("reporter", "test reporter", ""); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pg.DeleteAgent("reporter") })
	}
	t.Setenv("ARTEX_LIGHTPANDA_MCP_URL", url)
	reporter, _ := pg.GetAgentByKey("reporter")
	visible, _ := pg.AgentVisible(reporter.ID, "mcp")
	hadVisibility := false
	for _, id := range visible {
		hadVisibility = hadVisibility || id == lightpandaReaderMCPID
	}
	if err := pg.ToggleVisibility(reporter.ID, "mcp", lightpandaReaderMCPID, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.ToggleVisibility(reporter.ID, "mcp", lightpandaReaderMCPID, hadVisibility) })
	oldEnabled, hadEnabled, _ := pg.GetSetting(settingLightpandaEnabled)
	if err := pg.SetBool(settingLightpandaEnabled, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadEnabled {
			_ = pg.SetSetting(settingLightpandaEnabled, oldEnabled)
		} else {
			_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, settingLightpandaEnabled)
		}
	})
	wireAgentAugment(pg, t.TempDir(), nil)
	t.Cleanup(func() { agent.ToolAugment = nil })
	for _, key := range []string{"reporter", "worker"} {
		tools, _, cleanup := agent.ToolAugment(context.Background(), key)
		if cleanup != nil {
			defer cleanup()
		}
		found := 0
		for _, tool := range tools {
			if !strings.HasPrefix(tool.Name(), "mcp__"+lightpandaReaderMCPName+"__") {
				continue
			}
			found++
			if key != "reporter" || !isLightpandaReadTool(tool.Name()) {
				t.Errorf("%s received disallowed tool %s", key, tool.Name())
			}
		}
		if key == "reporter" && found != 8 {
			t.Errorf("reporter received %d read tools, want 8", found)
		}
	}
	if lightpandaRuntimePrompt(pg, reporter) == "" {
		t.Fatal("visible reporter should receive the runtime prompt")
	}
	if err := pg.SetBool(settingLightpandaEnabled, false); err != nil {
		t.Fatal(err)
	}
	if lightpandaMCP(pg).Enabled || lightpandaRuntimePrompt(pg, reporter) != "" {
		t.Fatal("disabled Lightpanda must remove tools and runtime prompt")
	}
	tools, _, cleanup := agent.ToolAugment(context.Background(), "reporter")
	if cleanup != nil {
		defer cleanup()
	}
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name(), "mcp__"+lightpandaReaderMCPName+"__") {
			t.Fatal("disabled Lightpanda remained visible to reporter")
		}
	}
}
