package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestTOTPMatchesRFCVectorAndTimeWindow(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if got := totpCode(secret, time.Unix(59, 0)); got != "287082" {
		t.Fatalf("TOTP at 59s = %q, want 287082", got)
	}
	if !validTOTP(secret, "287082", time.Unix(89, 0)) {
		t.Fatal("adjacent 30-second window should be accepted")
	}
	if validTOTP(secret, "287082", time.Unix(120, 0)) {
		t.Fatal("expired TOTP was accepted")
	}
	if validTOTP(secret, "abc082", time.Unix(59, 0)) || validTOTP(secret, "28708", time.Unix(59, 0)) {
		t.Fatal("non-six-digit TOTP was accepted")
	}
}

type totpTestStore struct {
	value string
	err   error
}

func (s totpTestStore) GetSetting(string) (string, bool, error) {
	return s.value, s.value != "", s.err
}

func TestLoginTOTPRequirementFailsClosed(t *testing.T) {
	key := []byte("test-only-signing-key-32-characters")
	at := time.Unix(59, 0)
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	stored, err := encryptTOTPSecret(key, secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, value, code                string
		readErr                          error
		wantRequired, wantValid, wantErr bool
	}{
		{"unset", "", "", nil, false, true, false},
		{"enabled missing code", stored, "", nil, true, false, false},
		{"enabled wrong code", stored, "123456", nil, true, false, false},
		{"enabled valid code", stored, "287082", nil, true, true, false},
		{"database failure", "", "", errors.New("db unavailable"), false, false, true},
		{"corrupt secret", "invalid", "", nil, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			required, valid, err := checkLoginTOTP(totpTestStore{tc.value, tc.readErr}, key, tc.code, at)
			if required != tc.wantRequired || valid != tc.wantValid || (err != nil) != tc.wantErr {
				t.Fatalf("required=%v valid=%v err=%v", required, valid, err)
			}
		})
	}
}

func TestTOTPSecretEncryptionRejectsTampering(t *testing.T) {
	key := []byte("test-only-signing-key-32-characters")
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	stored, err := encryptTOTPSecret(key, secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, secret) {
		t.Fatal("secret is visible in stored value")
	}
	got, err := decryptTOTPSecret(key, stored)
	if err != nil || got != secret {
		t.Fatalf("decrypt = %q, %v", got, err)
	}
	if _, err := decryptTOTPSecret(key, stored+"A"); err == nil {
		t.Fatal("tampered secret was accepted")
	}
}

func TestTOTPFailureLimit(t *testing.T) {
	var attempts totpAttempts
	now := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		if !attempts.allow(now) {
			t.Fatalf("attempt %d blocked too early", i+1)
		}
	}
	if attempts.allow(now) {
		t.Fatal("sixth attempt should be blocked")
	}
	if !attempts.allow(now.Add(time.Minute)) {
		t.Fatal("attempts should reset after a minute")
	}
}

func TestTOTPSettingsRoutesRequireAdmin(t *testing.T) {
	s := &Server{m: &Manager{}, jwtKey: []byte("test-only-signing-key-32-characters")}
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		path := "/api/settings/totp"
		if method == http.MethodPost {
			path += "/setup"
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without JWT = %d, want 401", method, path, w.Code)
		}
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s with JWT and no DB = %d, want 503", method, path, w.Code)
		}
	}
}

func TestTOTPSettingsAndLoginFlow(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	for _, key := range []string{authPassKey, totpSettingKey} {
		old, exists, err := m.pg.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		defer func(key, old string, exists bool) {
			if exists {
				_ = m.pg.SetSetting(key, old)
			} else {
				_, _ = m.pg.Exec("DELETE FROM settings WHERE key=$1", key)
			}
		}(key, old, exists)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("admin-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting(authPassKey, string(hash)); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting(totpSettingKey, ""); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, jwtKey: []byte("test-only-signing-key-32-characters")}
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if authorized {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	setup := request(http.MethodPost, "/api/settings/totp/setup", "{}", true)
	if setup.Code != 200 {
		t.Fatalf("setup = %d %s", setup.Code, setup.Body.String())
	}
	var enrollment struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}
	if err := json.Unmarshal(setup.Body.Bytes(), &enrollment); err != nil || len(enrollment.Secret) != 32 || !strings.HasPrefix(enrollment.URI, "otpauth://totp/") {
		t.Fatal("invalid enrollment response")
	}
	stored, _, err := m.pg.GetSetting(totpSettingKey)
	if err != nil || stored != "" {
		t.Fatal("setup must not enable TOTP before confirmation")
	}
	code := totpCode(enrollment.Secret, time.Now())
	badEnable := request(http.MethodPut, "/api/settings/totp", fmt.Sprintf(`{"password":"admin-password","secret":%q,"code":"abc"}`, enrollment.Secret), true)
	if badEnable.Code != 400 {
		t.Fatalf("bad enrollment code = %d", badEnable.Code)
	}
	stored, _, err = m.pg.GetSetting(totpSettingKey)
	if err != nil || stored != "" {
		t.Fatal("bad enrollment code enabled TOTP")
	}
	enable := request(http.MethodPut, "/api/settings/totp", fmt.Sprintf(`{"password":"admin-password","secret":%q,"code":%q}`, enrollment.Secret, code), true)
	if enable.Code != 200 {
		t.Fatalf("enable = %d %s", enable.Code, enable.Body.String())
	}
	status := request(http.MethodGet, "/api/settings/totp", "", true)
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"enabled":true`) || strings.Contains(status.Body.String(), enrollment.Secret) {
		t.Fatal("status must report enabled without exposing secret")
	}
	login := `{"username":"ARTEX","password":"admin-password"}`
	if w := request(http.MethodPost, "/api/auth/login", login, false); w.Code != 401 {
		t.Fatalf("login without TOTP = %d", w.Code)
	}
	withCode := fmt.Sprintf(`{"username":"ARTEX","password":"admin-password","totp":%q}`, code)
	if w := request(http.MethodPost, "/api/auth/login", withCode, false); w.Code != 200 {
		t.Fatalf("login with TOTP = %d %s", w.Code, w.Body.String())
	}
	badDisable := request(http.MethodDelete, "/api/settings/totp", `{"password":"admin-password","code":"abc"}`, true)
	if badDisable.Code != 400 {
		t.Fatalf("bad disable code = %d", badDisable.Code)
	}
	disable := request(http.MethodDelete, "/api/settings/totp", fmt.Sprintf(`{"password":"admin-password","code":%q}`, code), true)
	if disable.Code != 200 {
		t.Fatalf("disable = %d %s", disable.Code, disable.Body.String())
	}
	if w := request(http.MethodPost, "/api/auth/login", login, false); w.Code != 200 {
		t.Fatalf("login after disable = %d %s", w.Code, w.Body.String())
	}
}
