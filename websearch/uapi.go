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

const uapiEndpoint = "https://uapis.cn/api/v1/search/aggregate"

type uapiProvider struct {
	client   *http.Client
	endpoint string
	apiKey   string
}

// NewUAPI builds a permission-gated web_search tool backed by UAPI.
func NewUAPI(apiKey, proxy string) (actool.CoreTool, error) {
	p, err := newUAPIProvider(apiKey, proxy)
	if err != nil {
		return nil, err
	}
	return uapiTool(p), nil
}

func newUAPIProvider(apiKey, proxy string) (*uapiProvider, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("web_search: UAPI requires an API key")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// An empty proxy means direct egress, not the process's HTTP_PROXY setting.
	transport.Proxy = nil
	if proxy = strings.TrimSpace(proxy); proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5") {
			return nil, fmt.Errorf("web_search: invalid proxy URL")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	return &uapiProvider{
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
			// Never forward a bearer token to a redirect destination.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		endpoint: uapiEndpoint,
		apiKey:   apiKey,
	}, nil
}

func uapiTool(p *uapiProvider) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "web_search",
		Description: "Search the web and return a ranked list of results (title, URL, description) — metadata only, up to 5 by default. Use WebFetch to read the full content of a specific URL. This makes an external network request.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":      map[string]any{"type": "string", "description": "The search query; supports site: and filetype: operators."},
				"limit":      map[string]any{"type": "integer", "description": "Maximum number of results to return (1-20). Default 5.", "minimum": 1, "maximum": 20},
				"site":       map[string]any{"type": "string", "description": "Optional website/domain filter (without site:)."},
				"filetype":   map[string]any{"type": "string", "description": "Optional file type filter (without filetype:)."},
				"sort":       map[string]any{"type": "string", "enum": []string{"relevance", "date"}, "description": "Sort by relevance (default) or date."},
				"time_range": map[string]any{"type": "string", "enum": []string{"day", "week", "month", "year"}, "description": "Optional publication time range."},
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
				Query     string `json:"query"`
				Limit     int    `json:"limit"`
				Site      string `json:"site"`
				Filetype  string `json:"filetype"`
				Sort      string `json:"sort"`
				TimeRange string `json:"time_range"`
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
			filters := uapiFilters{Site: strings.TrimSpace(in.Site), Filetype: strings.TrimSpace(in.Filetype), Sort: strings.TrimSpace(in.Sort), TimeRange: strings.TrimSpace(in.TimeRange)}
			if err := filters.validate(); err != nil {
				return actool.Errorf("Error: " + err.Error()), nil
			}
			searchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			results, err := p.searchWithFilters(searchCtx, query, limit, filters)
			if err != nil {
				return actool.Errorf("Error searching web via uapi: " + err.Error()), nil
			}
			out, _ := json.MarshalIndent(map[string]any{"backend": "uapi", "query": query, "results": results}, "", "  ")
			if len(out) > 50000 {
				out = out[:50000]
			}
			return actool.Text(string(out)), nil
		},
	})
}

// ProbeUAPI checks the same endpoint and proxy without the tool permission layer.
func ProbeUAPI(ctx context.Context, apiKey, proxy, query string, limit int) ([]actool.SearchResult, error) {
	p, err := newUAPIProvider(apiKey, proxy)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 3
	}
	return p.search(ctx, query, limit)
}

type uapiFilters struct {
	Site      string `json:"site,omitempty"`
	Filetype  string `json:"filetype,omitempty"`
	Sort      string `json:"sort,omitempty"`
	TimeRange string `json:"time_range,omitempty"`
}

func (f uapiFilters) validate() error {
	if f.Sort != "" && f.Sort != "relevance" && f.Sort != "date" {
		return fmt.Errorf("sort must be relevance or date")
	}
	switch f.TimeRange {
	case "", "day", "week", "month", "year":
		return nil
	default:
		return fmt.Errorf("time_range must be day, week, month or year")
	}
}

func (p *uapiProvider) search(ctx context.Context, query string, limit int) ([]actool.SearchResult, error) {
	return p.searchWithFilters(ctx, query, limit, uapiFilters{})
}

func (p *uapiProvider) searchWithFilters(ctx context.Context, query string, limit int, filters uapiFilters) ([]actool.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query must not be empty")
	}
	if err := filters.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1
	}
	limit = min(limit, 20)
	body, _ := json.Marshal(struct {
		Query     string `json:"query"`
		FetchFull bool   `json:"fetch_full"`
		uapiFilters
	}{Query: query, FetchFull: false, uapiFilters: filters})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("could not create UAPI request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		// Transport errors can embed the request URL or proxy credentials.
		return nil, fmt.Errorf("UAPI request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("UAPI returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return nil, fmt.Errorf("could not read UAPI response")
	}
	var payload struct {
		Results json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || len(payload.Results) == 0 || string(payload.Results) == "null" {
		return nil, fmt.Errorf("could not parse UAPI response")
	}
	var hits []struct {
		Title    string `json:"title"`
		URL      string `json:"url"`
		Snippet  string `json:"snippet"`
		Position int    `json:"position"`
	}
	if err := json.Unmarshal(payload.Results, &hits); err != nil {
		return nil, fmt.Errorf("could not parse UAPI response")
	}
	results := make([]actool.SearchResult, 0, min(limit, len(hits)))
	for _, hit := range hits {
		if len(results) >= limit {
			break
		}
		if strings.TrimSpace(hit.URL) == "" {
			continue
		}
		position := hit.Position
		if position <= 0 {
			position = len(results) + 1
		}
		results = append(results, actool.SearchResult{
			Title: strings.TrimSpace(hit.Title), URL: strings.TrimSpace(hit.URL),
			Description: strings.TrimSpace(hit.Snippet), Position: position,
		})
	}
	return results, nil
}
