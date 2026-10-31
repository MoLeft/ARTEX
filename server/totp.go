package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

const totpSettingKey = "auth.totp_secret"

func totpCode(secret string, at time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(key) != 20 {
		return ""
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := int(sum[len(sum)-1] & 0xf)
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

func validTOTP(secret, code string, at time.Time) bool {
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	for _, skew := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, at.Add(skew))), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func totpCipher(key []byte) (cipher.AEAD, error) {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("artex/totp-secret/v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encryptTOTPSecret(key []byte, secret string) (string, error) {
	aead, err := totpCipher(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(secret), nil)), nil
}

func decryptTOTPSecret(key []byte, stored string) (string, error) {
	aead, err := totpCipher(key)
	if err != nil {
		return "", err
	}
	data, err := base64.RawURLEncoding.DecodeString(stored)
	if err != nil || len(data) < aead.NonceSize()+aead.Overhead() {
		return "", fmt.Errorf("invalid TOTP secret")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("invalid TOTP secret")
	}
	secret := string(plain)
	if totpCode(secret, time.Now()) == "" {
		return "", fmt.Errorf("invalid TOTP secret")
	}
	return secret, nil
}

type totpAttempts struct {
	// shortcut: the limit is per process; use shared storage if ARTEX runs multiple replicas.
	mu       sync.Mutex
	start    time.Time
	failures int
}

type totpSettingReader interface {
	GetSetting(string) (string, bool, error)
}

func checkLoginTOTP(store totpSettingReader, key []byte, code string, at time.Time) (required, valid bool, err error) {
	stored, _, err := store.GetSetting(totpSettingKey)
	if err != nil {
		return false, false, err
	}
	if stored == "" {
		return false, true, nil
	}
	secret, err := decryptTOTPSecret(key, stored)
	if err != nil {
		return true, false, err
	}
	return true, validTOTP(secret, code, at), nil
}

func (a *totpAttempts) allow(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.start.IsZero() || !now.Before(a.start.Add(time.Minute)) {
		a.start, a.failures = now, 0
	}
	if a.failures >= 5 {
		return false
	}
	a.failures++
	return true
}
