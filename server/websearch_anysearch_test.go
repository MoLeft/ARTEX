package server

import "testing"

func TestAnySearchManagerOptsWithoutKey(t *testing.T) {
	m := &Manager{webSearchOn: true, webSearchBackend: "anysearch", webSearchProxy: "http://localhost:8080"}
	opts := m.WebSearchOpts()
	if !opts.Enabled || opts.Backend != "anysearch" || opts.AnySearchKey != "" || opts.Proxy != m.webSearchProxy {
		t.Fatalf("anonymous AnySearch options: %+v", opts)
	}
	m.anySearchKey = "test-key"
	if opts = m.WebSearchOpts(); !opts.Enabled || opts.AnySearchKey != "test-key" {
		t.Fatalf("keyed AnySearch options: %+v", opts)
	}
	m.webSearchOn = false
	if opts = m.WebSearchOpts(); opts.Enabled {
		t.Fatalf("disabled search should stay disabled: %+v", opts)
	}
}
