package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/notify"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const ipWhitelistKey = "auth.ip_whitelist"
const loginFailureNotifyKey = "auth.login_failure_notify"

type loginAttemptLimiter struct {
	mu   sync.Mutex
	byIP map[string]struct {
		start time.Time
		count int
	}
}

func (l *loginAttemptLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byIP == nil {
		l.byIP = make(map[string]struct {
			start time.Time
			count int
		})
	}
	if len(l.byIP) > 1000 {
		l.byIP = make(map[string]struct {
			start time.Time
			count int
		})
	}
	now := time.Now()
	entry := l.byIP[ip]
	if now.Sub(entry.start) >= time.Minute {
		entry.start = now
		entry.count = 0
	}
	entry.count++
	l.byIP[ip] = entry
	return entry.count <= 10
}

func parseSessionJWT(token string, key []byte) (*jwt.RegisteredClaims, error) {
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return key, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithAudience("artex-session"), jwt.WithSubject("ARTEX"))
	if err != nil || claims.ID == "" {
		return nil, fmt.Errorf("invalid session token")
	}
	if _, err := uuid.Parse(claims.ID); err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *Server) issueSession(r *http.Request, factor string) (string, error) {
	if s.m == nil || s.m.pg == nil {
		return "", fmt.Errorf("database unavailable")
	}
	// Challenges and expired sessions no longer authenticate; prune them on login.
	_, _ = s.m.pg.ExecContext(r.Context(), `DELETE FROM auth_challenges WHERE expires_at<now()`)
	_, _ = s.m.pg.ExecContext(r.Context(), `DELETE FROM auth_sessions WHERE expires_at<now()`)
	id := uuid.NewString()
	token, err := signJWTWithID(s.jwtKey, id)
	if err != nil {
		return "", err
	}
	_, err = s.m.pg.ExecContext(r.Context(), `INSERT INTO auth_sessions(id,user_agent,ip_address,factor,expires_at) VALUES($1,$2,$3,$4,$5)`,
		id, r.UserAgent(), clientIP(r).String(), factor, time.Now().Add(jwtTTL))
	return token, err
}

func (s *Server) activeSession(r *http.Request) (string, error) {
	claims, err := parseSessionJWT(extractToken(r), s.jwtKey)
	if err != nil || s.m == nil || s.m.pg == nil {
		return "", fmt.Errorf("invalid session")
	}
	var active bool
	var lastSeen time.Time
	err = s.m.pg.QueryRowContext(r.Context(), `SELECT revoked_at IS NULL AND expires_at>now(),last_seen_at FROM auth_sessions WHERE id=$1`, claims.ID).Scan(&active, &lastSeen)
	if err == sql.ErrNoRows {
		// signJWT is also used by existing internal callers; a valid newly signed ID is registered once.
		_, err = s.m.pg.ExecContext(r.Context(), `INSERT INTO auth_sessions(id,user_agent,ip_address,expires_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			claims.ID, r.UserAgent(), clientIP(r).String(), claims.ExpiresAt.Time)
		if err == nil {
			err = s.m.pg.QueryRowContext(r.Context(), `SELECT revoked_at IS NULL AND expires_at>now(),last_seen_at FROM auth_sessions WHERE id=$1`, claims.ID).Scan(&active, &lastSeen)
		}
	}
	if err != nil || !active {
		return "", fmt.Errorf("revoked session")
	}
	if time.Since(lastSeen) > time.Minute {
		_, _ = s.m.pg.ExecContext(r.Context(), `UPDATE auth_sessions SET last_seen_at=now() WHERE id=$1`, claims.ID)
	}
	return claims.ID, nil
}

func (s *Server) beginTOTP(w http.ResponseWriter, r *http.Request, factor string) {
	id := uuid.NewString()
	data, _ := json.Marshal(map[string]string{"factor": factor})
	_, err := s.m.pg.ExecContext(r.Context(), `INSERT INTO auth_challenges(id,kind,data,expires_at) VALUES($1,'totp',$2,now()+interval '5 minutes')`, id, data)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"totp_required": true, "pending_token": id})
}

func (s *Server) finishTOTP(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	var req struct {
		PendingToken string `json:"pending_token"`
		Code         string `json:"code"`
	}
	if decode(r, &req) != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	id, err := uuid.Parse(req.PendingToken)
	if err != nil {
		writeErr(w, 401, "验证会话无效或已过期")
		return
	}
	var raw []byte
	err = s.m.pg.QueryRowContext(r.Context(), `SELECT data FROM auth_challenges WHERE id=$1 AND kind='totp' AND expires_at>now()`, id).Scan(&raw)
	if err != nil {
		writeErr(w, 401, "验证会话无效或已过期")
		return
	}
	required, valid, err := checkLoginTOTP(s.m.pg, s.jwtKey, req.Code, time.Now())
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !required || !valid {
		s.recordLoginFailure(r, "TOTP 验证码错误")
		if !s.totpAttempts.allow(time.Now()) {
			writeErr(w, 429, "验证码尝试过多，请一分钟后重试")
			return
		}
		writeErr(w, 401, "验证码错误")
		return
	}
	var payload struct {
		Factor string `json:"factor"`
	}
	if json.Unmarshal(raw, &payload) != nil || (payload.Factor != "password" && payload.Factor != "passkey") {
		writeErr(w, 401, "验证会话无效")
		return
	}
	result, err := s.m.pg.ExecContext(r.Context(), `DELETE FROM auth_challenges WHERE id=$1 AND kind='totp' AND expires_at>now()`, id)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		writeErr(w, 401, "验证会话已使用")
		return
	}
	if allowed, err := s.ipAllowed(r); err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	} else if !allowed {
		writeJSON(w, 403, map[string]any{"error": "当前 IP 不在白名单内", "code": "ip_blocked"})
		return
	}
	token, err := s.issueSession(r, payload.Factor)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"token": token})
}

type ipWhitelist struct {
	Enabled bool     `json:"enabled"`
	CIDRs   []string `json:"cidrs"`
}

func parseCIDR(raw string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(strings.TrimSpace(raw)); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}
func ipInList(addr netip.Addr, cidrs []string) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, raw := range cidrs {
		p, err := parseCIDR(raw)
		if err == nil && p.Contains(addr) {
			return true
		}
	}
	return false
}
func trustedProxy(addr netip.Addr) bool {
	if addr.IsLoopback() {
		return true
	}
	for _, raw := range strings.Split(os.Getenv("ARTEX_TRUSTED_PROXY_CIDRS"), ",") {
		p, err := parseCIDR(raw)
		if err == nil && p.Contains(addr) {
			return true
		}
	}
	return false
}
func clientIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if !trustedProxy(peer) {
		return peer
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0 && trustedProxy(peer); i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		peer = hop.Unmap()
	}
	return peer
}
func (s *Server) loadWhitelist() (ipWhitelist, error) {
	var cfg ipWhitelist
	if s.m == nil || s.m.pg == nil {
		return cfg, fmt.Errorf("database unavailable")
	}
	raw, _, err := s.m.pg.GetSetting(ipWhitelistKey)
	if err != nil || raw == "" {
		return cfg, err
	}
	err = json.Unmarshal([]byte(raw), &cfg)
	if err != nil {
		return cfg, err
	}
	if cfg.Enabled && len(cfg.CIDRs) == 0 {
		return cfg, fmt.Errorf("empty whitelist")
	}
	for _, cidr := range cfg.CIDRs {
		if _, err := parseCIDR(cidr); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}
func (s *Server) ipAllowed(r *http.Request) (bool, error) {
	cfg, err := s.loadWhitelist()
	if err != nil {
		return false, err
	}
	return !cfg.Enabled || ipInList(clientIP(r), cfg.CIDRs), nil
}
func (s *Server) getIPWhitelist(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadWhitelist()
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if cfg.CIDRs == nil {
		cfg.CIDRs = []string{}
	}
	writeJSON(w, 200, map[string]any{"enabled": cfg.Enabled, "cidrs": cfg.CIDRs, "current_ip": clientIP(r).String()})
}
func (s *Server) saveIPWhitelist(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	var req ipWhitelist
	if decode(r, &req) != nil || len(req.CIDRs) > 64 {
		writeErr(w, 400, "IP 段格式错误或数量超过 64")
		return
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, raw := range req.CIDRs {
		p, err := parseCIDR(raw)
		if err != nil {
			writeErr(w, 400, "IP 段格式错误："+raw)
			return
		}
		value := p.String()
		if !seen[value] {
			clean = append(clean, value)
			seen[value] = true
		}
	}
	req.CIDRs = clean
	if req.Enabled && (len(clean) == 0 || !ipInList(clientIP(r), clean)) {
		writeErr(w, 400, "开启白名单时必须包含当前访问 IP，避免锁定自己")
		return
	}
	data, _ := json.Marshal(req)
	if err := s.m.pg.SetSetting(ipWhitelistKey, string(data)); err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	s.getIPWhitelist(w, r)
}

func (s *Server) getSessions(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	current, err := s.activeSession(r)
	if err != nil {
		writeErr(w, 401, "未授权")
		return
	}
	rows, err := s.m.pg.QueryContext(r.Context(), `SELECT id::text,user_agent,ip_address,factor,created_at,last_seen_at FROM auth_sessions WHERE revoked_at IS NULL AND expires_at>now() ORDER BY last_seen_at DESC`)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	defer rows.Close()
	type item struct {
		ID         string    `json:"id"`
		UserAgent  string    `json:"user_agent"`
		IP         string    `json:"ip"`
		Factor     string    `json:"factor"`
		CreatedAt  time.Time `json:"created_at"`
		LastSeenAt time.Time `json:"last_seen_at"`
		Current    bool      `json:"current"`
	}
	items := []item{}
	for rows.Next() {
		var v item
		if rows.Scan(&v.ID, &v.UserAgent, &v.IP, &v.Factor, &v.CreatedAt, &v.LastSeenAt) != nil {
			writeErr(w, 503, errDataSourceUnavailable)
			return
		}
		v.Current = v.ID == current
		items = append(items, v)
	}
	if rows.Err() != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, map[string]any{"count": len(items), "sessions": items})
}
func (s *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	current, err := s.activeSession(r)
	if err != nil {
		writeErr(w, 401, "未授权")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if decode(r, &req) != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	required, valid, err := checkLoginTOTP(s.m.pg, s.jwtKey, req.Code, time.Now())
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if required && !valid {
		s.recordLoginFailure(r, "强制退出设备时 TOTP 验证失败")
		if !s.totpAttempts.allow(time.Now()) {
			writeErr(w, 429, "验证码尝试过多")
			return
		}
		writeErr(w, 401, "TOTP 验证码错误")
		return
	}
	result, err := s.m.pg.ExecContext(r.Context(), `UPDATE auth_sessions SET revoked_at=now() WHERE id<>$1 AND revoked_at IS NULL AND expires_at>now()`, current)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	count, _ := result.RowsAffected()
	writeJSON(w, 200, map[string]any{"revoked": count})
}
func (s *Server) getLoginFailureNotify(w http.ResponseWriter, _ *http.Request) {
	if s.pg(w) == nil {
		return
	}
	raw, _, err := s.m.pg.GetSetting(loginFailureNotifyKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": raw == "true"})
}
func (s *Server) saveLoginFailureNotify(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if decode(r, &req) != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	value := "false"
	if req.Enabled {
		value = "true"
	}
	if s.m.pg.SetSetting(loginFailureNotifyKey, value) != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, req)
}

func (s *Server) recordLoginFailure(r *http.Request, reason string) {
	if s.m == nil || s.m.pg == nil {
		return
	}
	enabled, _, err := s.m.pg.GetSetting(loginFailureNotifyKey)
	if err != nil || enabled != "true" {
		return
	}
	ip := clientIP(r).String()
	key := ip + ":" + reason
	s.securityEventMu.Lock()
	if s.securityEventLast == nil {
		s.securityEventLast = make(map[string]time.Time)
	}
	now := time.Now()
	if now.Sub(s.securityEventLast[key]) < time.Minute {
		s.securityEventMu.Unlock()
		return
	}
	if len(s.securityEventLast) > 1000 {
		s.securityEventLast = make(map[string]time.Time)
	}
	s.securityEventLast[key] = now
	s.securityEventMu.Unlock()
	snapshot := notify.Snapshot{Kind: notify.EventAuthFailed, Name: "账号安全：登录失败", VulnClass: "账号安全事件", Severity: "high", Summary: fmt.Sprintf("原因：%s；IP：%s；时间：%s；设备：%s", reason, ip, now.Format(time.RFC3339), notify.TruncateRunes(r.UserAgent(), 120))}
	data, _ := json.Marshal(snapshot)
	if _, err := s.m.pg.ExecContext(r.Context(), `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,0,$2)`, notify.EventAuthFailed, data); err != nil {
		log.Printf("[auth] 登录失败通知入队失败: %v", err)
	}
}
