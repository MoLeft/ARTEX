package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
)

const (
	settingProxyReconnectMax      = "proxy_reconnect_max_attempts"
	settingProxyReconnectInterval = "proxy_reconnect_interval_seconds"
	defaultProxyReconnectMax      = 5
	defaultProxyReconnectInterval = 30
)

// isProxyFailure deliberately requires a configured proxy at the call site and
// only matches transport failures. Provider quota/auth errors must keep their
// existing retry/failover behavior.
func isProxyFailure(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, part := range []string{"proxyconnect", "proxy connection", "proxy error", "connection refused", "connection reset", "network is unreachable", "broken pipe", "unexpected eof", "eof"} {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

type proxyReconnectSupervisor struct {
	mu   sync.Mutex
	wake chan struct{}
}

func (s *Server) ensureProxySupervisor() {
	s.proxyReconnect.mu.Lock()
	if s.proxyReconnect.wake == nil {
		s.proxyReconnect.wake = make(chan struct{}, 1)
		go s.runProxyReconnect(s.ctx)
	}
	s.proxyReconnect.mu.Unlock()
}

func (s *Server) signalProxySupervisor() {
	s.ensureProxySupervisor()
	select {
	case s.proxyReconnect.wake <- struct{}{}:
	default:
	}
}

func (s *Server) proxyReconnectConfig() (int, time.Duration) {
	max := defaultProxyReconnectMax
	interval := defaultProxyReconnectInterval
	if s == nil || s.m == nil || s.m.pg == nil {
		return max, time.Duration(interval) * time.Second
	}
	if v, ok, _ := s.m.pg.GetSetting(settingProxyReconnectMax); ok {
		if n, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &max); err != nil || n != 1 || max <= 0 {
			max = defaultProxyReconnectMax
		}
	}
	if v, ok, _ := s.m.pg.GetSetting(settingProxyReconnectInterval); ok {
		if n, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &interval); err != nil || n != 1 || interval <= 0 {
			interval = defaultProxyReconnectInterval
		}
	}
	return max, time.Duration(interval) * time.Second
}

func (s *Server) handleProxyFailure(taskID string, cause error) {
	if strings.TrimSpace(s.m.GlobalProxy()) == "" {
		return
	}
	if err := s.m.ApplyProxyPause(taskID); err != nil {
		log.Printf("[proxy] pause task %s: %v", taskID, err)
		return
	}
	if s.engine != nil {
		if t, ok := s.m.Task(taskID); ok && t.lifecycleSnapshot().PauseReason == "paused_by_proxy" {
			s.engine.Pause(taskID, agent.AbortPausedByProxy)
		}
	}
	s.cancelTaskChat(taskID, agent.AbortChatPausedWithTask)
	max, interval := s.proxyReconnectConfig()
	now := time.Now().Add(interval).Unix()
	for _, t := range s.m.List() {
		if t.ID != taskID && t.lifecycleSnapshot().PauseReason != "paused_by_proxy" {
			continue
		}
		t.updateLifecycle(func(st *taskLifecycleState) {
			st.ReconnectMax = max
			st.ReconnectNextAt = now
			st.ReconnectExhausted = false
			st.ReconnectLastError = truncateProxyError(cause)
		})
	}
	s.ensureProxySupervisor()
	s.signalProxySupervisor()
}

func truncateProxyError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

func (s *Server) runProxyReconnect(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.proxyReconnectTick()
		case <-s.proxyReconnect.wake:
			s.proxyReconnectTick()
		}
	}
}

func (s *Server) proxyReconnectTick() {
	if strings.TrimSpace(s.m.GlobalProxy()) == "" {
		return
	}
	now := time.Now().Unix()
	max, interval := s.proxyReconnectConfig()
	for _, t := range s.m.List() {
		st := t.lifecycleSnapshot()
		if st.PauseReason != "paused_by_proxy" || st.ReconnectExhausted || (st.ReconnectNextAt > 0 && st.ReconnectNextAt > now) {
			continue
		}
		st.ReconnectAttempt++
		st.ReconnectMax = max
		t.updateLifecycle(func(v *taskLifecycleState) {
			v.ReconnectAttempt = st.ReconnectAttempt
			v.ReconnectMax = max
			v.ReconnectNextAt = 0
		})
		res := probeEgress(context.Background(), s.m.GlobalProxy(), "", defaultEgressProbes, 10*time.Second)
		if res.OK {
			s.proxyReconnectSuccess()
			return
		}
		err := res.Error
		exhausted := st.ReconnectAttempt >= max
		next := time.Now().Add(interval).Unix()
		t.updateLifecycle(func(v *taskLifecycleState) {
			v.ReconnectNextAt = next
			v.ReconnectExhausted = exhausted
			v.ReconnectLastError = err
		})
		log.Printf("[proxy] reconnect attempt %d/%d failed: %s", st.ReconnectAttempt, max, err)
	}
}

func (s *Server) proxyReconnectSuccess() {
	for _, t := range s.m.List() {
		st := t.lifecycleSnapshot()
		if st.PauseReason != "paused_by_proxy" {
			continue
		}
		var err error
		if s.engine != nil {
			_, err = s.admitTaskWhenScheduled(t, "resume", true, false)
		} else {
			err = s.m.ClearPauseReason(t.ID)
		}
		if err != nil {
			log.Printf("[proxy] resume task %s: %v", t.ID, err)
			continue
		}
		if s.engine != nil {
			s.engine.Resume(t)
		}
		t.updateLifecycle(func(v *taskLifecycleState) {
			v.ReconnectAttempt = 0
			v.ReconnectMax = 0
			v.ReconnectNextAt = 0
			v.ReconnectExhausted = false
			v.ReconnectLastError = ""
		})
	}
}

func (s *Server) testTaskProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.m.Task(id)
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	proxy := strings.TrimSpace(s.m.GlobalProxy())
	if proxy == "" {
		writeJSON(w, 200, map[string]any{"ok": true, "direct": true})
		s.proxyReconnectSuccess()
		return
	}
	res := probeEgress(r.Context(), proxy, "", defaultEgressProbes, 10*time.Second)
	if res.OK {
		s.proxyReconnectSuccess()
	}
	if !res.OK && res.Error == "" {
		res.Error = "代理探测失败"
	}
	writeJSON(w, 200, map[string]any{"ok": res.OK, "error": res.Error, "task_id": t.ID})
}

func (s *Server) setProxyReconnectSettings(max, interval *int) error {
	if max != nil {
		if *max < 1 || *max > 1000 {
			return errors.New("proxy reconnect max must be 1..1000")
		}
		if err := s.m.pg.SetSetting(settingProxyReconnectMax, fmt.Sprint(*max)); err != nil {
			return err
		}
	}
	if interval != nil {
		if *interval < 1 || *interval > 86400 {
			return errors.New("proxy reconnect interval must be 1..86400 seconds")
		}
		if err := s.m.pg.SetSetting(settingProxyReconnectInterval, fmt.Sprint(*interval)); err != nil {
			return err
		}
	}
	return nil
}
