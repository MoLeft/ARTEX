package server

import (
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"net/url"
	"time"

	"github.com/Autumn-27/artex/db"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) totpPasswordValid(w http.ResponseWriter, pg *db.DB, password string) bool {
	hash, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return false
	}
	if hash == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		if !s.totpAttempts.allow(time.Now()) {
			writeErr(w, 429, "验证尝试过多，请一分钟后重试")
			return false
		}
		writeErr(w, 400, "密码或验证码错误")
		return false
	}
	return true
}

func (s *Server) getTOTPSettings(w http.ResponseWriter, _ *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	stored, _, err := pg.GetSetting(totpSettingKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if stored != "" {
		if _, err := decryptTOTPSecret(s.jwtKey, stored); err != nil {
			writeErr(w, 503, "TOTP 配置不可用，请联系服务器管理员")
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"enabled": stored != ""})
}

func (s *Server) setupTOTP(w http.ResponseWriter, _ *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	stored, _, err := pg.GetSetting(totpSettingKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if stored != "" {
		writeErr(w, 409, "TOTP 已启用")
		return
	}
	secretBytes := make([]byte, 20)
	if _, err := rand.Read(secretBytes); err != nil {
		writeErr(w, 500, "生成 TOTP 密钥失败")
		return
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)
	u := url.URL{Scheme: "otpauth", Host: "totp", Path: "/ARTEX:ARTEX"}
	u.RawQuery = url.Values{"secret": {secret}, "issuer": {"ARTEX"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"secret": secret, "uri": u.String()})
}

func (s *Server) enableTOTP(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Password string `json:"password"`
		Secret   string `json:"secret"`
		Code     string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if !s.totpPasswordValid(w, pg, req.Password) {
		return
	}
	if len(req.Secret) != 32 || !validTOTP(req.Secret, req.Code, time.Now()) {
		writeErr(w, 400, "密钥或验证码无效，请检查验证器时间")
		return
	}
	stored, err := encryptTOTPSecret(s.jwtKey, req.Secret)
	if err != nil {
		writeErr(w, 500, "TOTP 配置加密失败")
		return
	}
	inserted, err := pg.SetSettingIfEmpty(totpSettingKey, stored)
	if err != nil {
		writeErr(w, 503, "TOTP 配置保存失败")
		return
	}
	if !inserted {
		writeErr(w, 409, "TOTP 已启用，请刷新页面")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"enabled": true})
}

func (s *Server) disableTOTP(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if !s.totpPasswordValid(w, pg, req.Password) {
		return
	}
	stored, _, err := pg.GetSetting(totpSettingKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if stored == "" {
		writeErr(w, 409, "TOTP 尚未启用")
		return
	}
	secret, err := decryptTOTPSecret(s.jwtKey, stored)
	if err != nil {
		writeErr(w, 503, "TOTP 配置不可用，请联系服务器管理员")
		return
	}
	if !validTOTP(secret, req.Code, time.Now()) {
		if !s.totpAttempts.allow(time.Now()) {
			writeErr(w, 429, "验证尝试过多，请一分钟后重试")
			return
		}
		writeErr(w, 400, "密码或验证码错误")
		return
	}
	cleared, err := pg.ClearSettingIfValue(totpSettingKey, stored)
	if err != nil {
		writeErr(w, 503, "TOTP 配置保存失败")
		return
	}
	if !cleared {
		writeErr(w, 409, "TOTP 配置已变化，请刷新页面")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"enabled": false})
}
