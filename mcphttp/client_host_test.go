package mcphttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamableHTTPHostOverride(t *testing.T) {
	deleted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1" {
			http.Error(w, "Host not allowed", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.ID == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Mcp-Session-Id", "test-session")
		result := any(map[string]any{"tools": []any{}})
		if req.Method == "initialize" {
			result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "test", "version": "1"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer srv.Close()

	url := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	if _, err := New(context.Background(), "test", url, nil, false); err == nil {
		t.Fatal("hostname should be rejected without a Host override")
	}
	client, err := New(context.Background(), "test", url, map[string]string{"Host": "127.0.0.1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Tools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil || !deleted {
		t.Fatalf("session close: %v, deleted=%v", err, deleted)
	}
}
