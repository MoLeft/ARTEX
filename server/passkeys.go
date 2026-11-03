package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const passkeyOriginKey = "auth.passkey_origin"
const passkeyUserIDKey = "auth.passkey_user_id"

type passkeyUser struct {
	id          []byte
	credentials []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.id }
func (u passkeyUser) WebAuthnName() string                       { return "ARTEX" }
func (u passkeyUser) WebAuthnDisplayName() string                { return "ARTEX" }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (s *Server) passkeyCipher() (cipher.AEAD, error) {
	mac := hmac.New(sha256.New, s.jwtKey)
	mac.Write([]byte("artex/passkey-credential/v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (s *Server) sealCredential(credential *webauthn.Credential) ([]byte, error) {
	plain, err := json.Marshal(credential)
	if err != nil {
		return nil, err
	}
	aead, err := s.passkeyCipher()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, nil), nil
}
func (s *Server) openCredential(data []byte) (webauthn.Credential, error) {
	var c webauthn.Credential
	aead, err := s.passkeyCipher()
	if err != nil {
		return c, err
	}
	if len(data) < aead.NonceSize()+aead.Overhead() {
		return c, fmt.Errorf("invalid credential")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(plain, &c)
	return c, err
}
func (s *Server) passkeyOrigin() (string, error) {
	raw, _, err := s.m.pg.GetSetting(passkeyOriginKey)
	return raw, err
}
func originForRegistration(r *http.Request) (string, error) {
	raw := r.Header.Get("Origin")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !strings.EqualFold(u.Host, r.Host) {
		return "", fmt.Errorf("origin mismatch")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return "", fmt.Errorf("passkeys need HTTPS")
	}
	return u.Scheme + "://" + u.Host, nil
}
func (s *Server) webauthnFor(origin string) (*webauthn.WebAuthn, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid passkey origin")
	}
	return webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "ARTEX", RPOrigins: []string{origin}, AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}})
}
func (s *Server) passkeyUser(create bool) (passkeyUser, error) {
	var user passkeyUser
	raw, _, err := s.m.pg.GetSetting(passkeyUserIDKey)
	if err != nil {
		return user, err
	}
	if raw == "" && create {
		id := make([]byte, 32)
		if _, err = rand.Read(id); err != nil {
			return user, err
		}
		raw = base64.RawURLEncoding.EncodeToString(id)
		if _, err = s.m.pg.InsertSettingIfAbsent(passkeyUserIDKey, raw); err != nil {
			return user, err
		}
		raw, _, err = s.m.pg.GetSetting(passkeyUserIDKey)
		if err != nil {
			return user, err
		}
	}
	user.id, err = base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(user.id) == 0 {
		return user, fmt.Errorf("passkey user unavailable")
	}
	rows, err := s.m.pg.Query(`SELECT credential FROM auth_passkeys ORDER BY created_at`)
	if err != nil {
		return user, err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return user, err
		}
		c, e := s.openCredential(data)
		if e != nil {
			return user, e
		}
		user.credentials = append(user.credentials, c)
	}
	return user, rows.Err()
}
func (s *Server) listPasskeys(w http.ResponseWriter, _ *http.Request) {
	if s.pg(w) == nil {
		return
	}
	rows, err := s.m.pg.Query(`SELECT id,remark,created_at,last_used_at FROM auth_passkeys ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	defer rows.Close()
	type item struct {
		ID         string     `json:"id"`
		Remark     string     `json:"remark"`
		CreatedAt  time.Time  `json:"created_at"`
		LastUsedAt *time.Time `json:"last_used_at"`
	}
	items := []item{}
	for rows.Next() {
		var id []byte
		var v item
		if rows.Scan(&id, &v.Remark, &v.CreatedAt, &v.LastUsedAt) != nil {
			writeErr(w, 503, errDataSourceUnavailable)
			return
		}
		v.ID = base64.RawURLEncoding.EncodeToString(id)
		items = append(items, v)
	}
	if rows.Err() != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	origin, _ := s.passkeyOrigin()
	writeJSON(w, 200, map[string]any{"origin": origin, "passkeys": items})
}
func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	current, err := s.activeSession(r)
	if err != nil {
		writeErr(w, 401, "未授权")
		return
	}
	var req struct {
		Remark   string `json:"remark"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if decode(r, &req) != nil || strings.TrimSpace(req.Remark) == "" || len(req.Remark) > 100 {
		writeErr(w, 400, "备注不能为空且不能超过 100 字节")
		return
	}
	required, valid, err := checkLoginTOTP(s.m.pg, s.jwtKey, req.Code, time.Now())
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if required {
		if !valid {
			s.recordLoginFailure(r, "注册通行密钥时 TOTP 验证失败")
			if !s.totpAttempts.allow(time.Now()) {
				writeErr(w, 429, "验证码尝试过多，请一分钟后重试")
				return
			}
			writeErr(w, 401, "TOTP 验证码错误")
			return
		}
	} else {
		hash, _, err := s.m.pg.GetSetting(authPassKey)
		if err != nil {
			writeErr(w, 503, errDataSourceUnavailable)
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
			s.recordLoginFailure(r, "注册通行密钥时密码错误")
			writeErr(w, 401, "密码错误")
			return
		}
	}
	origin, err := originForRegistration(r)
	if err != nil {
		writeErr(w, 400, "通行密钥需要 HTTPS 且只能从当前站点配置")
		return
	}
	saved, err := s.passkeyOrigin()
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if saved != "" && saved != origin {
		writeErr(w, 409, "通行密钥已绑定到另一站点："+saved)
		return
	}
	if saved == "" {
		if _, err = s.m.pg.InsertSettingIfAbsent(passkeyOriginKey, origin); err != nil {
			writeErr(w, 503, errDataSourceUnavailable)
			return
		}
		saved, err = s.passkeyOrigin()
		if err != nil || saved != origin {
			writeErr(w, 409, "通行密钥站点已变化")
			return
		}
	}
	wa, err := s.webauthnFor(origin)
	if err != nil {
		writeErr(w, 503, "通行密钥配置无效")
		return
	}
	user, err := s.passkeyUser(true)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	options, session, err := wa.BeginRegistration(user)
	if err != nil {
		writeErr(w, 500, "无法创建通行密钥挑战")
		return
	}
	data, _ := json.Marshal(struct {
		Session *webauthn.SessionData `json:"session"`
		Remark  string                `json:"remark"`
	}{session, strings.TrimSpace(req.Remark)})
	id := uuid.NewString()
	_, err = s.m.pg.ExecContext(r.Context(), `INSERT INTO auth_challenges(id,kind,data,session_id,expires_at) VALUES($1,'passkey-register',$2,$3,now()+interval '5 minutes')`, id, data, current)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, map[string]any{"challenge_id": id, "options": options})
}
func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	current, err := s.activeSession(r)
	if err != nil {
		writeErr(w, 401, "未授权")
		return
	}
	id, err := uuid.Parse(r.URL.Query().Get("challenge_id"))
	if err != nil {
		writeErr(w, 400, "挑战无效")
		return
	}
	var raw []byte
	err = s.m.pg.QueryRowContext(r.Context(), `DELETE FROM auth_challenges WHERE id=$1 AND kind='passkey-register' AND session_id=$2 AND expires_at>now() RETURNING data`, id, current).Scan(&raw)
	if err != nil {
		writeErr(w, 401, "挑战无效或已过期")
		return
	}
	var challenge struct {
		Session webauthn.SessionData `json:"session"`
		Remark  string               `json:"remark"`
	}
	if json.Unmarshal(raw, &challenge) != nil {
		writeErr(w, 500, "挑战数据无效")
		return
	}
	origin, err := s.passkeyOrigin()
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	wa, err := s.webauthnFor(origin)
	if err != nil {
		writeErr(w, 503, "通行密钥配置无效")
		return
	}
	user, err := s.passkeyUser(false)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	credential, err := wa.FinishRegistration(user, challenge.Session, r)
	if err != nil {
		writeErr(w, 401, "通行密钥注册验证失败")
		return
	}
	sealed, err := s.sealCredential(credential)
	if err != nil {
		writeErr(w, 500, "保存通行密钥失败")
		return
	}
	_, err = s.m.pg.ExecContext(r.Context(), `INSERT INTO auth_passkeys(id,remark,credential) VALUES($1,$2,$3)`, credential.ID, challenge.Remark, sealed)
	if err != nil {
		writeErr(w, 409, "通行密钥已存在或保存失败")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	if !s.loginAttempts.allow(clientIP(r).String()) {
		writeErr(w, 429, "登录尝试过多，请一分钟后重试")
		return
	}
	origin, err := s.passkeyOrigin()
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if origin == "" {
		writeErr(w, 404, "尚未设置通行密钥")
		return
	}
	wa, err := s.webauthnFor(origin)
	if err != nil {
		writeErr(w, 503, "通行密钥配置无效")
		return
	}
	user, err := s.passkeyUser(false)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if len(user.credentials) == 0 {
		writeErr(w, 404, "尚未设置通行密钥")
		return
	}
	options, session, err := wa.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeErr(w, 500, "无法创建通行密钥挑战")
		return
	}
	data, _ := json.Marshal(session)
	id := uuid.NewString()
	_, err = s.m.pg.ExecContext(r.Context(), `INSERT INTO auth_challenges(id,kind,data,expires_at) VALUES($1,'passkey-login',$2,now()+interval '5 minutes')`, id, data)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, map[string]any{"challenge_id": id, "options": options})
}
func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, err := uuid.Parse(r.URL.Query().Get("challenge_id"))
	if err != nil {
		s.recordLoginFailure(r, "不受支持的通行密钥")
		writeErr(w, 401, "挑战无效")
		return
	}
	var raw []byte
	err = s.m.pg.QueryRowContext(r.Context(), `DELETE FROM auth_challenges WHERE id=$1 AND kind='passkey-login' AND expires_at>now() RETURNING data`, id).Scan(&raw)
	if err != nil {
		s.recordLoginFailure(r, "不受支持的通行密钥")
		writeErr(w, 401, "挑战无效或已过期")
		return
	}
	var session webauthn.SessionData
	if json.Unmarshal(raw, &session) != nil {
		writeErr(w, 500, "挑战数据无效")
		return
	}
	origin, err := s.passkeyOrigin()
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	wa, err := s.webauthnFor(origin)
	if err != nil {
		writeErr(w, 503, "通行密钥配置无效")
		return
	}
	user, err := s.passkeyUser(false)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	credential, err := wa.FinishLogin(user, session, r)
	if err != nil {
		s.recordLoginFailure(r, "不受支持的通行密钥")
		writeErr(w, 401, "通行密钥验证失败")
		return
	}
	sealed, err := s.sealCredential(credential)
	if err != nil {
		writeErr(w, 500, "通行密钥更新失败")
		return
	}
	result, err := s.m.pg.ExecContext(r.Context(), `UPDATE auth_passkeys SET credential=$2,last_used_at=now() WHERE id=$1`, credential.ID, sealed)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		s.recordLoginFailure(r, "不受支持的通行密钥")
		writeErr(w, 401, "通行密钥已删除")
		return
	}
	required, _, err := checkLoginTOTP(s.m.pg, s.jwtKey, "", time.Now())
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if required {
		s.beginTOTP(w, r, "passkey")
		return
	}
	if allowed, err := s.ipAllowed(r); err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	} else if !allowed {
		writeJSON(w, 403, map[string]any{"error": "当前 IP 不在白名单内", "code": "ip_blocked"})
		return
	}
	token, err := s.issueSession(r, "passkey")
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, map[string]any{"token": token})
}
func (s *Server) updatePasskey(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, err := base64.RawURLEncoding.DecodeString(r.PathValue("id"))
	if err != nil || len(id) == 0 {
		writeErr(w, 400, "通行密钥 ID 无效")
		return
	}
	var req struct {
		Remark string `json:"remark"`
		Code   string `json:"code"`
	}
	if decode(r, &req) != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if !s.verifySettingsTOTP(w, r, req.Code) {
		return
	}
	if r.Method == http.MethodDelete {
		result, err := s.m.pg.ExecContext(r.Context(), `DELETE FROM auth_passkeys WHERE id=$1`, id)
		if err != nil {
			writeErr(w, 503, errDataSourceUnavailable)
			return
		}
		if n, _ := result.RowsAffected(); n == 0 {
			writeErr(w, 404, "通行密钥不存在")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if strings.TrimSpace(req.Remark) == "" || len(req.Remark) > 100 {
		writeErr(w, 400, "备注不能为空且不能超过 100 字节")
		return
	}
	result, err := s.m.pg.ExecContext(r.Context(), `UPDATE auth_passkeys SET remark=$2 WHERE id=$1`, id, strings.TrimSpace(req.Remark))
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeErr(w, 404, "通行密钥不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
