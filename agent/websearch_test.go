package agent

import (
	"testing"

	"github.com/Autumn-27/norma/agentcore"
)

func TestConfigureAnySearch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		config    WebSearchOpts
		wantLocal bool
	}{
		{"anonymous", WebSearchOpts{Enabled: true, Backend: "anysearch"}, true},
		{"keyed", WebSearchOpts{Enabled: true, Backend: "anysearch", AnySearchKey: "test-key"}, true},
		{"disabled", WebSearchOpts{Enabled: false, Backend: "anysearch"}, false},
		{"existing provider", WebSearchOpts{Enabled: true, Backend: "ddgs"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := agentcore.Options{EnableWebSearch: tc.config.Enabled}
			if err := configureWebSearch(&opts, tc.config); err != nil {
				t.Fatal(err)
			}
			if tc.wantLocal {
				if opts.EnableWebSearch || len(opts.Tools) != 1 || opts.Tools[0].Name() != "web_search" {
					t.Fatalf("AnySearch should have exactly one local tool: %+v", opts)
				}
			} else if opts.EnableWebSearch != tc.config.Enabled || len(opts.Tools) != 0 {
				t.Fatalf("original backend setting changed: %+v", opts)
			}
		})
	}
}
