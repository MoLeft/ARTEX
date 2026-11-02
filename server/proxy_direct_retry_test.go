package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Autumn-27/artex/traffic"
)

func TestProxyDirectRetrySettingDefaultsOffAndPersists(t *testing.T) {
	seed, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("isolated postgres unavailable: %v", err)
	}
	old, existed, err := seed.pg.GetSetting(settingProxyDirectRetry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.pg.Exec("DELETE FROM settings WHERE key=$1", settingProxyDirectRetry); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if existed {
			_ = m.pg.SetSetting(settingProxyDirectRetry, old)
		} else {
			_, _ = m.pg.Exec("DELETE FROM settings WHERE key=$1", settingProxyDirectRetry)
		}
		_ = m.Close()
	}()
	if m.ProxyDirectRetry() {
		t.Fatal("missing setting must default off")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	check := func(method, body string) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, "/api/settings", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		if method == http.MethodGet {
			s.getSettings(w, r)
		} else {
			s.putSettings(w, r)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("%s settings: %d %s", method, w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := check(http.MethodGet, "")["proxy_direct_retry_on_connect_failure"]; got != false {
		t.Fatalf("default API value=%v", got)
	}
	if got := check(http.MethodPut, `{"proxy_direct_retry_on_connect_failure":true}`)["proxy_direct_retry_on_connect_failure"]; got != true {
		t.Fatalf("saved API value=%v", got)
	}
	if !m.ProxyDirectRetry() {
		t.Fatal("setting not applied to live Manager")
	}
	fresh, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if !fresh.ProxyDirectRetry() {
		t.Fatal("setting did not survive Manager restart")
	}
}

func TestProxyRouteByCaptureMode(t *testing.T) {
	egress, err := traffic.OpenEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer egress.Close()
	mitm, err := traffic.Open(t.TempDir(), "127.0.0.1:39873")
	if err != nil {
		t.Fatal(err)
	}
	defer mitm.Close()
	m := &Manager{traffic: mitm, egress: egress}
	for _, tc := range []struct {
		global  string
		capture bool
		want    string
	}{
		{"", false, ""},
		{"", true, mitm.ProxyAddr()},
		{"http://127.0.0.1:18080", false, egress.Addr()},
		{"http://127.0.0.1:18080", true, mitm.ProxyAddr()},
	} {
		m.globalProxy, m.trafficOn = tc.global, tc.capture
		if got := m.ProxyAddr(); got != tc.want {
			t.Errorf("proxy=%q capture=%v: got %q want %q", tc.global, tc.capture, got, tc.want)
		}
	}
}

func TestGlobalHTTPToolsUseEgressProxy(t *testing.T) {
	egress, err := traffic.OpenEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer egress.Close()
	m := &Manager{globalProxy: "http://127.0.0.1:18080", egress: egress}
	s := &Server{m: m}
	request := &http.Request{URL: &url.URL{Scheme: "http", Host: "example.org"}}
	for _, spec := range []httpExec{{UseRecordingProxy: true}, {Proxy: m.globalProxy}} {
		tr := s.httpProxyTransport(spec)
		if tr == nil {
			t.Fatal("global HTTP tool has no proxy transport")
		}
		got, err := tr.Proxy(request)
		if err != nil || got.String() != egress.Addr() {
			t.Fatalf("global HTTP tool proxy=%v error=%v", got, err)
		}
	}
	tr := s.httpProxyTransport(httpExec{Proxy: "http://127.0.0.1:18081"})
	got, err := tr.Proxy(request)
	if err != nil || got.String() != "http://127.0.0.1:18081" {
		t.Fatalf("explicit custom proxy=%v error=%v", got, err)
	}
}
