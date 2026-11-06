package server

import "testing"

func TestUAPIManagerOptsRequireKey(t *testing.T) {
	m := &Manager{webSearchOn: true, webSearchBackend: "uapi", webSearchProxy: "http://localhost:8080"}
	if opts := m.WebSearchOpts(); opts.Enabled {
		t.Fatalf("UAPI must remain off without a key: %+v", opts)
	}
	m.uapiKey = "test-key"
	if opts := m.WebSearchOpts(); !opts.Enabled || opts.Backend != "uapi" || opts.UAPIKey != "test-key" || opts.Proxy != m.webSearchProxy {
		t.Fatalf("keyed UAPI options: %+v", opts)
	}
	m.webSearchOn = false
	if opts := m.WebSearchOpts(); opts.Enabled {
		t.Fatalf("disabled search should stay disabled: %+v", opts)
	}
}
