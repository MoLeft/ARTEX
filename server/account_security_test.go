package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestIPWhitelistAddressesAndTrustedProxy(t *testing.T) {
	cases := []struct {
		ip     string
		ranges []string
		want   bool
	}{
		{"192.0.2.8", []string{"192.0.2.0/24"}, true},
		{"192.0.3.8", []string{"192.0.2.0/24"}, false},
		{"2001:db8::5", []string{"2001:db8::/32"}, true},
		{"2001:db9::5", []string{"2001:db8::/32"}, false},
		{"::ffff:192.0.2.8", []string{"192.0.2.0/24"}, true},
	}
	for _, tc := range cases {
		addr, err := parseCIDR(tc.ip)
		if err != nil {
			t.Fatal(err)
		}
		if got := ipInList(addr.Addr(), tc.ranges); got != tc.want {
			t.Fatalf("%s in %v = %v", tc.ip, tc.ranges, got)
		}
	}
	if _, err := parseCIDR("192.0.2.1/999"); err == nil {
		t.Fatal("invalid prefix accepted")
	}
	direct := httptest.NewRequest("GET", "/", nil)
	direct.RemoteAddr = "198.51.100.5:1234"
	direct.Header.Set("X-Forwarded-For", "192.0.2.9")
	if got := clientIP(direct).String(); got != "198.51.100.5" {
		t.Fatalf("untrusted XFF spoofed IP: %s", got)
	}
	proxy := httptest.NewRequest("GET", "/", nil)
	proxy.RemoteAddr = "127.0.0.1:1234"
	proxy.Header.Set("X-Forwarded-For", "192.0.2.9, 198.51.100.5")
	if got := clientIP(proxy).String(); got != "198.51.100.5" {
		t.Fatalf("trusted proxy chain = %s", got)
	}
}

func TestPendingTokenCannotBeAdminJWT(t *testing.T) {
	key := []byte("test-only-signing-key-32-characters")
	token, err := signJWT(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = parseSessionJWT(token, key); err != nil {
		t.Fatal(err)
	}
	if _, err = parseSessionJWT("pending-token", key); err == nil {
		t.Fatal("pending token accepted as admin JWT")
	}
	claims, err := parseSessionJWT(token, key)
	if err != nil || claims.ExpiresAt.Time.Before(time.Now()) {
		t.Fatal("session expired immediately")
	}
}

func TestPasskeyOriginAndEncryption(t *testing.T) {
	r := httptest.NewRequest("POST", "https://example.com/api/settings/passkeys/register/begin", nil)
	r.Header.Set("Origin", "https://example.com")
	if origin, err := originForRegistration(r); err != nil || origin != "https://example.com" {
		t.Fatalf("valid origin: %s %v", origin, err)
	}
	r.Header.Set("Origin", "https://evil.example")
	if _, err := originForRegistration(r); err == nil {
		t.Fatal("cross-origin passkey setup accepted")
	}
	r.Header.Set("Origin", "http://example.com")
	if _, err := originForRegistration(r); err == nil {
		t.Fatal("insecure origin accepted")
	}
	s := &Server{jwtKey: []byte("test-only-signing-key-32-characters")}
	sealed, err := s.sealCredential(&webauthn.Credential{ID: []byte("test-key")})
	if err != nil { t.Fatal(err) }
	opened, err := s.openCredential(sealed)
	if err != nil || string(opened.ID) != "test-key" { t.Fatalf("credential round trip: %v", err) }
	sealed[len(sealed)-1] ^= 1
	if _, err := s.openCredential(sealed); err == nil { t.Fatal("tampered credential accepted") }
}
