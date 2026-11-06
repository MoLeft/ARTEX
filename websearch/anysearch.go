// Package websearch provides search backends not yet included in Norma.
package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

const (
	anySearchEndpoint = "https://api.anysearch.com/v1/search"
	maxResponseBytes  = 2 << 20
)

type anySearchProvider struct {
	client   *http.Client
	endpoint string
	apiKey   string
}

// NewAnySearch builds the same permission-gated web_search tool as Norma, backed
// by AnySearch. An API key is optional: without one, AnySearch uses anonymous quota.
func NewAnySearch(apiKey, proxy string) (actool.CoreTool, error) {
	p, err := newAnySearchProvider(apiKey, proxy)
	if err != nil {
		return nil, err
	}
	return anySearchTool(p), nil
}

func anySearchTool(p *anySearchProvider) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "web_search",
		Description: "Search the web and return a ranked list of results (title, URL, description) — metadata only, up to 5 by default. Use WebFetch to read the full content of a specific URL. This makes an external network request.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "The search query."},
				"limit": map[string]any{"type": "integer", "description": "Maximum number of results to return (1-20). Default 5.", "minimum": 1, "maximum": 20},
			},
			"required": []any{"query"},
		},
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(_ context.Context, input json.RawMessage, _ permission.Context) permission.Decision {
			var in struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(input, &in)
			return permission.AskUser("web search: " + in.Query)
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var in struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return actool.Result{}, err
			}
			query := strings.TrimSpace(in.Query)
			if query == "" {
				return actool.Errorf("Error: query must not be empty"), nil
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 5
			}
			if limit > 20 {
				limit = 20
			}
			searchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			results, err := p.search(searchCtx, query, limit)
			if err != nil {
				return actool.Errorf("Error searching web via anysearch: " + err.Error()), nil
			}
			out, _ := json.MarshalIndent(map[string]any{"backend": "anysearch", "query": query, "results": results}, "", "  ")
			if len(out) > 50000 {
				out = out[:50000]
			}
			return actool.Text(string(out)), nil
		},
	})
}

// Probe runs the same backend for the settings-page connectivity check.
func Probe(ctx context.Context, apiKey, proxy, query string, limit int) ([]actool.SearchResult, error) {
	p, err := newAnySearchProvider(apiKey, proxy)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 3
	}
	return p.search(ctx, query, limit)
}

func newAnySearchProvider(apiKey, proxy string) (*anySearchProvider, error) {
	client := &http.Client{}
	if proxy = strings.TrimSpace(proxy); proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5") {
			return nil, fmt.Errorf("web_search: invalid proxy URL")
		}
		client.Transport = &http.Transport{Proxy: http.ProxyURL(parsed)}
	}
	return &anySearchProvider{client: client, endpoint: anySearchEndpoint, apiKey: strings.TrimSpace(apiKey)}, nil
}

func (p *anySearchProvider) search(ctx context.Context, query string, limit int) ([]actool.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query must not be empty")
	}
	if limit < 1 {
		limit = 1
	}
	// The public tool allows 20 results, but AnySearch caps max_results at 10.
	count := min(limit, 10)
	body, err := json.Marshal(struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}{query, count})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Error messages, especially anonymous quota responses, can contain newly
	// generated credentials. Never include response bodies in returned errors.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("AnySearch returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Code *int `json:"code"`
		Data struct {
			Results []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Snippet string `json:"snippet"`
				Content string `json:"content"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("could not parse AnySearch response")
	}
	if payload.Code == nil || *payload.Code != 0 {
		return nil, fmt.Errorf("AnySearch returned an unsuccessful response")
	}
	results := make([]actool.SearchResult, 0, min(count, len(payload.Data.Results)))
	for _, hit := range payload.Data.Results {
		if len(results) >= count {
			break
		}
		if strings.TrimSpace(hit.URL) == "" {
			continue
		}
		description := strings.TrimSpace(hit.Snippet)
		if description == "" {
			description = strings.TrimSpace(hit.Content)
		}
		results = append(results, actool.SearchResult{
			Title: strings.TrimSpace(hit.Title), URL: hit.URL,
			Description: description, Position: len(results) + 1,
		})
	}
	return results, nil
}
