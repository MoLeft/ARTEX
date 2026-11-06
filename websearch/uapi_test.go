package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

func testUAPI(t *testing.T, handler http.HandlerFunc) *uapiProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &uapiProvider{client: srv.Client(), endpoint: srv.URL, apiKey: "secret-token"}
}

func TestUAPIRequestAndResults(t *testing.T) {
	p := testUAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected method or content headers: %s, %s, %s", r.Method, r.Header.Get("Content-Type"), r.Header.Get("Accept"))
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q", got)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("request JSON: %v", err)
		}
		if len(request) != 2 || request["query"] != "test query" || request["fetch_full"] != false {
			t.Errorf("request (must have query and fetch_full only): %+v", request)
		}
		_, _ = w.Write([]byte(`{"results":[{"title":" One ","url":"https://example.com","snippet":" Summary ","position":7},{"title":"No URL","snippet":"skip"},{"title":"Two","url":"https://example.org","snippet":"Other","position":2}]}`))
	})
	got, err := p.search(context.Background(), " test query ", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (actool.SearchResult{Title: "One", URL: "https://example.com", Description: "Summary", Position: 7}) || got[1] != (actool.SearchResult{Title: "Two", URL: "https://example.org", Description: "Other", Position: 2}) {
		t.Fatalf("results: %+v", got)
	}
}

func TestUAPIFilters(t *testing.T) {
	p := testUAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]any{"query": "test", "fetch_full": false, "site": "go.dev", "filetype": "pdf", "sort": "date", "time_range": "week"} {
			if request[key] != want {
				t.Errorf("%s = %v, want %v", key, request[key], want)
			}
		}
		if len(request) != 6 {
			t.Errorf("unexpected fields: %+v", request)
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	tool := uapiTool(p)
	out, err := tool.Call(context.Background(), json.RawMessage(`{"query":"test","site":"go.dev","filetype":"pdf","sort":"date","time_range":"week"}`), nil)
	if err != nil || out.IsError {
		t.Fatalf("filtered search: %+v %v", out, err)
	}
	out, err = tool.Call(context.Background(), json.RawMessage(`{"query":"test","sort":"invalid"}`), nil)
	if err != nil || !out.IsError {
		t.Fatalf("invalid sort: %+v %v", out, err)
	}
}

func TestUAPILimitsAreLocal(t *testing.T) {
	p := testUAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if len(request) != 2 {
			t.Errorf("unexpected limit/page in request: %+v", request)
		}
		results := make([]map[string]any, 25)
		for i := range results {
			results[i] = map[string]any{"title": "Title", "url": fmt.Sprintf("https://example.com/%d", i), "position": i + 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	for _, tc := range []struct{ limit, want int }{{1, 1}, {20, 20}, {30, 20}} {
		got, err := p.search(context.Background(), "query", tc.limit)
		if err != nil || len(got) != tc.want {
			t.Fatalf("limit %d: got %d, error %v", tc.limit, len(got), err)
		}
	}
}

func TestUAPIToolContract(t *testing.T) {
	p := testUAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if len(request) != 2 {
			t.Errorf("unexpected request fields: %+v", request)
		}
		results := make([]map[string]string, 22)
		for i := range results {
			results[i] = map[string]string{"url": fmt.Sprintf("https://example.com/%d", i)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	tool := uapiTool(p)
	if tool.Name() != "web_search" || !tool.IsReadOnly(nil) || !tool.IsConcurrencySafe(nil) {
		t.Fatal("unexpected tool identity or capability")
	}
	if d := tool.CheckPermissions(context.Background(), json.RawMessage(`{"query":"example"}`), permission.Context{}); d.Behavior != permission.Ask {
		t.Fatalf("permission: %+v", d)
	}
	for _, tc := range []struct {
		input string
		count int
	}{{`{"query":"example"}`, 5}, {`{"query":"example","limit":100}`, 20}} {
		out, err := tool.Call(context.Background(), json.RawMessage(tc.input), nil)
		if err != nil || out.IsError {
			t.Fatalf("tool call: %+v, %v", out, err)
		}
		var result struct {
			Backend string                `json:"backend"`
			Query   string                `json:"query"`
			Results []actool.SearchResult `json:"results"`
		}
		if err := json.Unmarshal([]byte(out.Flatten()), &result); err != nil || result.Backend != "uapi" || result.Query != "example" || len(result.Results) != tc.count {
			t.Fatalf("tool output: %+v, %v", result, err)
		}
	}
	out, err := tool.Call(context.Background(), json.RawMessage(`{"query":" "}`), nil)
	if err != nil || !out.IsError {
		t.Fatalf("empty query: %+v, %v", out, err)
	}
}

func TestUAPIConfig(t *testing.T) {
	for _, proxy := range []string{"", "socks5://localhost:1080", "http://localhost:8080", "https://localhost:8080"} {
		tool, err := NewUAPI(" test-key ", proxy)
		if err != nil || tool.Name() != "web_search" {
			t.Fatalf("proxy %q: %v, %v", proxy, tool, err)
		}
	}
	if _, err := NewUAPI(" ", ""); err == nil {
		t.Fatal("expected missing key error")
	}
	if _, err := ProbeUAPI(context.Background(), "", "", "query", 2); err == nil {
		t.Fatal("expected probe missing key error")
	}
	if _, err := NewUAPI("test-key", "bad-proxy"); err == nil {
		t.Fatal("expected invalid proxy error")
	}
}

func TestUAPIProxyIsIndependent(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "http://unresolvable.invalid/search" || r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("proxy request: %s, auth %q", r.URL, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer proxy.Close()
	p, err := newUAPIProvider("secret-token", proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.endpoint = "http://unresolvable.invalid/search"
	if _, err := p.search(context.Background(), "query", 5); err != nil {
		t.Fatal(err)
	}
}

func TestUAPIErrorsRemainOpaque(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `{"error":"secret-token"}`},
		{"rate limit", 429, `{"error":"secret-token"}`},
		{"business error", 200, `{"error":"secret-token"}`},
		{"malformed", 200, `bad json secret-token`},
		{"missing results", 200, `{}`},
		{"wrong results", 200, `{"results":{"error":"secret-token"}}`},
		{"oversized", 200, `{"results":[]}` + strings.Repeat(" ", maxResponseBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testUAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := p.search(context.Background(), "query", 2)
			if err == nil || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("error leaked response or missing: %v", err)
			}
		})
	}
	p := &uapiProvider{endpoint: "https://uapis.cn/api/v1/search/aggregate", apiKey: "secret-token", client: &http.Client{Transport: errorTransport{}}}
	_, err := p.search(context.Background(), "query", 2)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("transport error leaked key: %v", err)
	}
}

type errorTransport struct{}

func (errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("Bearer secret-token")
}

func TestUAPIContextCancellation(t *testing.T) {
	p := testUAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.search(ctx, "query", 5)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("cancelled request: %v", err)
	}
}
