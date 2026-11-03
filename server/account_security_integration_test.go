package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestAccountSessionsWhitelistAndSecurityEvents(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	if _, err := m.pg.Exec(`DELETE FROM auth_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`DELETE FROM notification_events WHERE kind='auth_failed'`); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("admin-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{authPassKey: string(hash), totpSettingKey: "", ipWhitelistKey: "", loginFailureNotifyKey: "false"} {
		if err := m.pg.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{m: m, jwtKey: []byte("test-only-signing-key-32-characters")}
	h := s.Handler()
	request := func(method, path, body, token, ip, ua string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = ip + ":1234"
		r.Header.Set("User-Agent", ua)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := func(ip, ua string) string {
		w := request("POST", "/api/auth/login", `{"username":"ARTEX","password":"admin-password"}`, "", ip, ua)
		if w.Code != 200 {
			t.Fatalf("login %s: %d %s", ip, w.Code, w.Body.String())
		}
		var v struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Token == "" {
			t.Fatal(w.Body.String())
		}
		return v.Token
	}
	first := login("192.0.2.1", "Test Mac")
	second := login("198.51.100.2", "Test iOS")
	w := request("GET", "/api/auth/sessions", "", first, "192.0.2.1", "Test Mac")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"count":2`) || !strings.Contains(w.Body.String(), "Test iOS") {
		t.Fatalf("sessions: %d %s", w.Code, w.Body.String())
	}
	w = request("PUT", "/api/settings/ip-whitelist", `{"enabled":true,"cidrs":["192.0.2.0/24","2001:db8::/32"]}`, first, "192.0.2.1", "Test Mac")
	if w.Code != 200 {
		t.Fatalf("enable whitelist: %d %s", w.Code, w.Body.String())
	}
	w = request("GET", "/api/auth/sessions", "", second, "198.51.100.2", "Test iOS")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "ip_blocked") {
		t.Fatalf("out-of-range session not blocked: %d %s", w.Code, w.Body.String())
	}
	w = request("POST", "/api/auth/sessions/revoke-others", `{}`, first, "192.0.2.1", "Test Mac")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"revoked":1`) {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	w = request("GET", "/api/auth/sessions", "", second, "192.0.2.1", "Test iOS")
	if w.Code != 401 {
		t.Fatalf("revoked session accepted: %d", w.Code)
	}
	w = request("PUT", "/api/settings/ip-whitelist", `{"enabled":false,"cidrs":[]}`, first, "192.0.2.1", "Test Mac")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request("PUT", "/api/settings/login-failure-notify", `{"enabled":true}`, first, "192.0.2.1", "Test Mac")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request("POST", "/api/auth/login", `{"username":"ARTEX","password":"wrong-password"}`, "", "203.0.113.9", "Attacker")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}
	var count int
	if err := m.pg.QueryRow(`SELECT count(*) FROM notification_events WHERE kind='auth_failed'`).Scan(&count); err != nil || count < 1 {
		t.Fatalf("security event not queued: count=%d err=%v", count, err)
	}
}

func TestPasskeyChallengeBoundToSessionAndOneUse(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte("admin-password"), bcrypt.MinCost)
	_ = m.pg.SetSetting(authPassKey, string(hash))
	_ = m.pg.SetSetting(totpSettingKey, "")
	s := &Server{m: m, jwtKey: []byte("test-only-signing-key-32-characters")}
	token, err := s.issueSession(httptest.NewRequest("GET", "https://example.com/", nil), "password")
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	begin := httptest.NewRequest("POST", "https://example.com/api/settings/passkeys/register/begin", strings.NewReader(`{"remark":"Test key","password":"admin-password"}`))
	begin.Header.Set("Origin", "https://example.com")
	begin.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, begin)
	if w.Code != 200 {
		t.Fatalf("begin: %d %s", w.Code, w.Body.String())
	}
	var v struct {
		ChallengeID string `json:"challenge_id"`
	}
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.ChallengeID == "" {
		t.Fatal(w.Body.String())
	}
	for i := 0; i < 2; i++ {
		finish := httptest.NewRequest("POST", "https://example.com/api/settings/passkeys/register/finish?challenge_id="+v.ChallengeID, strings.NewReader(`{}`))
		finish.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, finish)
		if w.Code == 200 {
			t.Fatal("invalid passkey credential accepted")
		}
		if i == 1 && !strings.Contains(w.Body.String(), "挑战无效") {
			t.Fatalf("challenge reusable: %s", w.Body.String())
		}
	}
}

func TestPasskeyRegistrationLoginStillRequiresTOTP(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	_, _ = m.pg.Exec(`DELETE FROM auth_passkeys`)
	hash, _ := bcrypt.GenerateFromPassword([]byte("admin-password"), bcrypt.MinCost)
	_ = m.pg.SetSetting(authPassKey, string(hash))
	_ = m.pg.SetSetting(totpSettingKey, "")
	_ = m.pg.SetSetting(passkeyOriginKey, "https://example.com")
	_ = m.pg.SetSetting(ipWhitelistKey, "")
	s := &Server{m: m, jwtKey: []byte("test-only-signing-key-32-characters")}
	adminToken, err := s.issueSession(httptest.NewRequest("GET", "https://example.com/", nil), "password")
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://example.com"+path, strings.NewReader(body))
		r.Header.Set("Origin", "https://example.com")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	type beginResult struct {
		ChallengeID string `json:"challenge_id"`
		Options     struct {
			PublicKey struct {
				Challenge string `json:"challenge"`
			} `json:"publicKey"`
		} `json:"options"`
	}
	begin := request("/api/settings/passkeys/register/begin", `{"remark":"Mac test key","password":"admin-password"}`, adminToken)
	var reg beginResult
	if begin.Code != 200 || json.Unmarshal(begin.Body.Bytes(), &reg) != nil || reg.ChallengeID == "" {
		t.Fatalf("register begin: %d %s", begin.Code, begin.Body.String())
	}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := []byte("artex-test-passkey-identifier")
	coseKey, err := webauthncbor.Marshal(map[int64]any{1: int64(2), 3: int64(-7), -1: int64(1), -2: private.X.FillBytes(make([]byte, 32)), -3: private.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	attested := make([]byte, 16)
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(credentialID)))
	attested = append(attested, credentialID...)
	attested = append(attested, coseKey...)
	authData := func(flags byte, count uint32, tail []byte) []byte {
		rp := sha256.Sum256([]byte("example.com"))
		out := append([]byte{}, rp[:]...)
		out = append(out, flags)
		out = binary.BigEndian.AppendUint32(out, count)
		return append(out, tail...)
	}
	registrationAuth := authData(byte(protocol.FlagUserPresent|protocol.FlagUserVerified|protocol.FlagAttestedCredentialData), 0, attested)
	attestation, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": registrationAuth})
	if err != nil {
		t.Fatal(err)
	}
	clientData := func(kind, challenge string) []byte {
		b, _ := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": "https://example.com", "crossOrigin": false})
		return b
	}
	encoded := func(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }
	registerBody, _ := json.Marshal(map[string]any{"id": encoded(credentialID), "rawId": encoded(credentialID), "type": "public-key", "response": map[string]any{"attestationObject": encoded(attestation), "clientDataJSON": encoded(clientData("webauthn.create", reg.Options.PublicKey.Challenge))}})
	finish := request("/api/settings/passkeys/register/finish?challenge_id="+reg.ChallengeID, string(registerBody), adminToken)
	if finish.Code != 200 {
		t.Fatalf("register finish: %d %s", finish.Code, finish.Body.String())
	}
	secretBytes := []byte("12345678901234567890")
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)
	encrypted, err := encryptTOTPSecret(s.jwtKey, secret)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.pg.SetSetting(totpSettingKey, encrypted); err != nil {
		t.Fatal(err)
	}
	checkSettings := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://example.com"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+adminToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	goodCode := totpCode(secret, time.Now())
	badCode := "0" + goodCode[1:]
	if goodCode[0] == '0' {
		badCode = "1" + goodCode[1:]
	}
	badWhitelist := checkSettings("PUT", "/api/settings/ip-whitelist", `{"enabled":false,"cidrs":[],"code":"`+badCode+`"}`)
	if badWhitelist.Code != 401 {
		t.Fatalf("whitelist accepted wrong TOTP: %d %s", badWhitelist.Code, badWhitelist.Body.String())
	}
	goodWhitelist := checkSettings("PUT", "/api/settings/ip-whitelist", `{"enabled":false,"cidrs":[],"code":"`+goodCode+`"}`)
	if goodWhitelist.Code != 200 {
		t.Fatalf("whitelist rejected valid TOTP: %d %s", goodWhitelist.Code, goodWhitelist.Body.String())
	}
	keyPath := "/api/settings/passkeys/" + base64.RawURLEncoding.EncodeToString(credentialID)
	badRename := checkSettings("PUT", keyPath, `{"remark":"renamed","code":"`+badCode+`"}`)
	if badRename.Code != 401 {
		t.Fatalf("passkey rename accepted wrong TOTP: %d %s", badRename.Code, badRename.Body.String())
	}
	goodRename := checkSettings("PUT", keyPath, `{"remark":"renamed","code":"`+goodCode+`"}`)
	if goodRename.Code != 200 {
		t.Fatalf("passkey rename rejected valid TOTP: %d %s", goodRename.Code, goodRename.Body.String())
	}
	loginBegin := request("/api/auth/passkey/login/begin", `{}`, "")
	var challenge beginResult
	if loginBegin.Code != 200 || json.Unmarshal(loginBegin.Body.Bytes(), &challenge) != nil {
		t.Fatalf("passkey begin: %d %s", loginBegin.Code, loginBegin.Body.String())
	}
	assertionAuth := authData(byte(protocol.FlagUserPresent|protocol.FlagUserVerified), 1, nil)
	assertionClient := clientData("webauthn.get", challenge.Options.PublicKey.Challenge)
	digest := sha256.Sum256(assertionClient)
	signed := append(append([]byte{}, assertionAuth...), digest[:]...)
	signedDigest := sha256.Sum256(signed)
	signature, err := ecdsa.SignASN1(rand.Reader, private, signedDigest[:])
	if err != nil {
		t.Fatal(err)
	}
	assertionBody, _ := json.Marshal(map[string]any{"id": encoded(credentialID), "rawId": encoded(credentialID), "type": "public-key", "response": map[string]any{"authenticatorData": encoded(assertionAuth), "clientDataJSON": encoded(assertionClient), "signature": encoded(signature), "userHandle": nil}})
	loginFinish := request("/api/auth/passkey/login/finish?challenge_id="+challenge.ChallengeID, string(assertionBody), "")
	var pending struct {
		PendingToken string `json:"pending_token"`
		Token        string `json:"token"`
	}
	if loginFinish.Code != 200 || json.Unmarshal(loginFinish.Body.Bytes(), &pending) != nil || pending.PendingToken == "" || pending.Token != "" {
		t.Fatalf("passkey must require TOTP: %d %s", loginFinish.Code, loginFinish.Body.String())
	}
	totpBody, _ := json.Marshal(map[string]string{"pending_token": pending.PendingToken, "code": totpCode(secret, time.Now())})
	complete := request("/api/auth/totp", string(totpBody), "")
	if complete.Code != 200 || !strings.Contains(complete.Body.String(), `"token"`) {
		t.Fatalf("passkey TOTP completion: %d %s", complete.Code, complete.Body.String())
	}
	badDelete := checkSettings("DELETE", keyPath, `{"code":"`+badCode+`"}`)
	if badDelete.Code != 401 {
		t.Fatalf("passkey deletion accepted wrong TOTP: %d %s", badDelete.Code, badDelete.Body.String())
	}
	goodDelete := checkSettings("DELETE", keyPath, `{"code":"`+goodCode+`"}`)
	if goodDelete.Code != 200 {
		t.Fatalf("passkey deletion rejected valid TOTP: %d %s", goodDelete.Code, goodDelete.Body.String())
	}
}
