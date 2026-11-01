package selfupdate

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchLatestUsesForkRelease(t *testing.T) {
	client := &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.String(); got != "https://api.github.com/repos/MoLeft/ARTEX/releases/latest" {
			t.Fatalf("unexpected update source: %s", got)
		}
		if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("invalid GitHub request: %s %v", r.Method, r.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{
			"tag_name":"v0.26.10.31.001",
			"assets":[
				{"name":"artex-0.26.10.31.001-linux-arm64.zip","browser_download_url":"https://github.com/MoLeft/ARTEX/releases/download/v0.26.10.31.001/artex-0.26.10.31.001-linux-arm64.zip"},
				{"name":"SHA256SUMS","browser_download_url":"https://github.com/MoLeft/ARTEX/releases/download/v0.26.10.31.001/SHA256SUMS"}
			]
		}`))}, nil
	})}
	rel, err := FetchLatest(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{AssetName(rel.TagName, "linux", "arm64"), sumsAsset} {
		asset, ok := rel.FindAsset(name)
		if !ok {
			t.Fatalf("missing release asset %s", name)
		}
		body, err := get(context.Background(), &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("asset"))}, nil
		})}, asset.URL)
		if err != nil {
			t.Fatalf("fork asset URL rejected: %v", err)
		}
		body.Close()
	}
}

func TestFetchLatestMissingRelease(t *testing.T) {
	client := &http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	_, err := FetchLatest(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "MoLeft/ARTEX") || !strings.Contains(err.Error(), "不可访问或尚未发布") {
		t.Fatalf("unexpected missing-release error: %v", err)
	}
}

func TestFetchLatestBetaSelectsNewestVersion(t *testing.T) {
	client := &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != releasesURL {
			t.Fatalf("unexpected update source: %s", r.URL)
		}
		body := `[
			{"tag_name":"v0.26.10.31.001", "prerelease":false},
			{"tag_name":"v0.26.10.31.002-beta2", "prerelease":true},
			{"tag_name":"v0.26.10.31.003", "draft":true},
			{"tag_name":"v0.26.10.31.002-beta1", "prerelease":true},
			{"tag_name":"v0.26.10.31.004-beta", "prerelease":true}
		]`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	rel, err := FetchLatestBeta(context.Background(), client)
	if err != nil || rel.TagName != "v0.26.10.31.004-beta" {
		t.Fatalf("latest beta = %+v, %v", rel, err)
	}
}
