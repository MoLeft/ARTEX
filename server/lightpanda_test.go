package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestLightpandaReporterToolBoundary(t *testing.T) {
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
		if !isReporterLightpandaTool(name) {
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
		if isReporterLightpandaTool(name) {
			t.Errorf("non-read tool %s was allowed", name)
		}
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
			if key != "reporter" || !isReporterLightpandaTool(tool.Name()) {
				t.Errorf("%s received disallowed tool %s", key, tool.Name())
			}
		}
		if key == "reporter" && found != 8 {
			t.Errorf("reporter received %d read tools, want 8", found)
		}
	}
}
