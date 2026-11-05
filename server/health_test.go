package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthAndVersionAccess(t *testing.T) {
	s, _ := basicAuthTestServer(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, "/api/health", nil))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s health: status=%d headers=%v", method, w.Code, w.Header())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body) != 1 || body["ok"] != true {
			t.Fatalf("%s health exposed extra fields: %s (%v)", method, w.Body.String(), err)
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, exposed := body["version"]; w.Code != http.StatusUnauthorized || exposed {
		t.Fatalf("anonymous version: status=%d body=%s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.version(w, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	var versionBody map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &versionBody); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || versionBody["version"] != BuildVersion || len(versionBody) != 1 {
		t.Fatalf("authorized version handler: status=%d body=%s", w.Code, w.Body.String())
	}
	if got := saveBasicAuthForTest(s, `{"enabled":true,"username":"entry","password":"test-pass"}`); got.Code != http.StatusOK {
		t.Fatalf("enable Basic Auth: %s", got.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, "/api/health", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s health bypassed Basic Auth: %d", method, w.Code)
		}
	}
	jwt, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	r.Header.Set("Authorization", "Bearer "+jwt)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("JWT bypassed Basic Auth for version: %d", w.Code)
	}
}
