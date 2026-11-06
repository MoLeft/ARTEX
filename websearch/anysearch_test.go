package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func testAnySearch(t *testing.T, handler http.HandlerFunc) *anySearchProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &anySearchProvider{client: srv.Client(), endpoint: srv.URL}
}

func TestAnySearchAnonymousAndKeyed(t *testing.T) {
	for _, tc := range []struct{ name, key, wantAuth string }{
		{"anonymous", "", ""}, {"keyed", "a-secret", "Bearer a-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testAnySearch(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected request: %s %s", r.Method, r.Header.Get("Content-Type"))
				}
				if got := r.Header.Get("Authorization"); got != tc.wantAuth {
					t.Errorf("Authorization = %q", got)
				}
				var input struct {
					Query      string `json:"query"`
					MaxResults int    `json:"max_results"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Errorf("request JSON: %v", err)
				}
				if input.Query != "test query" || input.MaxResults != 3 {
					t.Errorf("request: %+v", input)
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{"results":[{"title":"One","url":"https://example.com","snippet":"Summary"},{"title":"Two","url":"https://example.org","content":"Fallback"},{"title":"No URL"}]}}`))
			})
			p.apiKey = tc.key
			got, err := p.search(context.Background(), "test query", 3)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0] != (actool.SearchResult{Title: "One", URL: "https://example.com", Description: "Summary", Position: 1}) || got[1].Description != "Fallback" || got[1].Position != 2 {
				t.Fatalf("results: %+v", got)
			}
		})
	}
}

func TestAnySearchCapsResultsAtTen(t *testing.T) {
	p := testAnySearch(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			MaxResults int `json:"max_results"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.MaxResults != 10 {
			t.Errorf("max_results = %d", input.MaxResults)
		}
		results := make([]map[string]string, 15)
		for i := range results {
			results[i] = map[string]string{"title": "Title", "url": fmt.Sprintf("https://example.com/%d", i)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"results": results}})
	})
	got, err := p.search(context.Background(), "query", 20)
	if err != nil || len(got) != 10 {
		t.Fatalf("results: %d, error: %v", len(got), err)
	}
}

func TestAnySearchErrorsDoNotExposeBody(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"quota", 402, `{"code":-1,"message":"generated api_key secret-token"}`},
		{"rate limit", 429, `{"code":-1,"message":"hidden detail"}`},
		{"business error", 200, `{"code":-1,"message":"generated api_key secret-token"}`},
		{"malformed", 200, `not-json secret-token`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testAnySearch(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := p.search(context.Background(), "query", 2)
			if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "hidden detail") {
				t.Fatalf("error leaked response or missing: %v", err)
			}
		})
	}
}

func TestAnySearchToolCall(t *testing.T) {
	p := testAnySearch(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			MaxResults int `json:"max_results"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.MaxResults != 5 {
			t.Errorf("default limit = %d", input.MaxResults)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"results":[{"title":"Example","url":"https://example.com","snippet":"A summary"}]}}`))
	})
	tool := anySearchTool(p)
	out, err := tool.Call(context.Background(), json.RawMessage(`{"query":"example"}`), nil)
	if err != nil || out.IsError || !strings.Contains(out.Flatten(), `"backend": "anysearch"`) || !strings.Contains(out.Flatten(), `"description": "A summary"`) {
		t.Fatalf("tool call: %+v, %v", out, err)
	}
	out, err = tool.Call(context.Background(), json.RawMessage(`{"query":" "}`), nil)
	if err != nil || !out.IsError {
		t.Fatalf("empty query: %+v, %v", out, err)
	}
}

func TestAnySearchToolConfig(t *testing.T) {
	for _, proxy := range []string{"", "socks5://localhost:1080", "http://localhost:8080"} {
		got, err := NewAnySearch("", proxy)
		if err != nil || got.Name() != "web_search" {
			t.Fatalf("proxy %q: %v, %v", proxy, got, err)
		}
	}
	if _, err := NewAnySearch("", "bad-proxy"); err == nil {
		t.Fatal("expected invalid proxy error")
	}
}
