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

// isProxyFailure is used for LLM/planner errors, where the error has no trusted
// indication that it came from the global target proxy. Keep this deliberately
// narrow: generic EOF/reset/refused errors belong to the model_error retry path.
func isProxyFailure(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, part := range []string{"proxyconnect", "proxy connection", "proxy error", "proxy handshake", "socks handshake", "failed to connect to proxy", "connect to proxy"} {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

// isGlobalProxyDialFailure is only used by the MITM upstream callback. That
// callback is on the actual configured global-proxy path; the saved proxy is
// still probed before any task is paused, so a target-side RequestError cannot
// pause tasks while the proxy itself remains healthy.
func isGlobalProxyDialFailure(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, part := range []string{"proxyconnect", "proxy connection", "proxy error", "proxy handshake", "socks", "connect to proxy", "upstream proxy", "connection refused", "connection reset", "network is unreachable", "broken pipe", "unexpected eof", "eof"} {
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
	if probe := probeEgress(context.Background(), s.m.GlobalProxy(), "", defaultEgressProbes, 10*time.Second); probe.OK {
		return
	}
	s.pauseProxyTask(taskID, cause)
}

func (s *Server) pauseProxyTask(taskID string, cause error) {
	if strings.TrimSpace(s.m.GlobalProxy()) == "" {
		return
	}
	transitioned, err := s.m.ApplyProxyPauseWithCallback(taskID, func() {
		if s.engine != nil {
			s.engine.Pause(taskID, agent.AbortPausedByProxy)
		}
	})
	if err != nil {
		log.Printf("[proxy] pause task %s: %v", taskID, err)
		return
	}
	if !transitioned {
		// A no-op means the task was already paused (possibly by a user,
		// schedule, or an exhausted proxy attempt). Preserve its reason and
		// reconnect state exactly as-is.
		return
	}
	s.cancelTaskChat(taskID, agent.AbortChatPausedWithTask)
	max, interval := s.proxyReconnectConfig()
	now := time.Now().Add(interval).Unix()
	if t, ok := s.m.Task(taskID); ok {
		t.updateLifecycle(func(st *taskLifecycleState) {
			// Only the task that transitioned in this call starts a new
			// reconnect round. A task that exhausted a prior round remains
			// untouched until it is manually resumed and paused again.
			st.ReconnectMax = max
			st.ReconnectNextAt = now
			st.ReconnectExhausted = false
			st.ReconnectLastError = truncateProxyError(cause)
		})
	}
	s.ensureProxySupervisor()
	s.signalProxySupervisor()
}

func (s *Server) handleProxyTrafficFailure(cause error) {
	if !isGlobalProxyDialFailure(cause) || strings.TrimSpace(s.m.GlobalProxy()) == "" {
		return
	}
	// Confirm the saved global proxy itself before pausing. MITM RequestError can
	// also represent a target-side failure, and those must not pause a task.
	if probe := probeEgress(context.Background(), s.m.GlobalProxy(), "", defaultEgressProbes, 10*time.Second); probe.OK {
		return
	}
	for _, t := range s.m.List() {
		st := t.lifecycleSnapshot()
		if isTerminalStatus(st.Status) || st.Paused || s.engine.IsDeleting(t.ID) {
			continue
		}
		s.pauseProxyTask(t.ID, cause)
	}
}

func (s *Server) handleProxyToolFailure(ctx context.Context, cause error) {
	if !isGlobalProxyDialFailure(cause) || strings.TrimSpace(s.m.GlobalProxy()) == "" {
		return
	}
	if probe := probeEgress(context.Background(), s.m.GlobalProxy(), "", defaultEgressProbes, 10*time.Second); probe.OK {
		return
	}
	if info := agent.RunInfoFrom(ctx); info.TaskID > 0 {
		s.pauseProxyTask(fmt.Sprint(info.TaskID), cause)
	}
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
		if isTerminalStatus(st.Status) {
			t.updateLifecycle(func(v *taskLifecycleState) {
				if v.PauseReason == "paused_by_proxy" {
					v.PauseReason = ""
				}
				v.ReconnectAttempt, v.ReconnectMax, v.ReconnectNextAt = 0, 0, 0
				v.ReconnectExhausted, v.ReconnectLastError = false, ""
			})
			continue
		}
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
	s.proxyReconnectSuccessWithHook(nil)
}

// proxyReconnectSuccessWithHook is kept small and private so tests can
// deterministically place a competing pause immediately after admission has
// released concMu. Production callers use proxyReconnectSuccess without a
// hook.
func (s *Server) proxyReconnectSuccessWithHook(afterAdmission func()) {
	max, interval := s.proxyReconnectConfig()
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	for _, t := range s.m.List() {
		if st := t.lifecycleSnapshot(); st.PauseReason != "paused_by_proxy" {
			continue
		}
		if s.m.pg != nil {
			inside, scheduleErr := s.proxyResumeInsideSchedule(t.ID, time.Now())
			if scheduleErr != nil {
				log.Printf("[proxy] schedule gate for task %s: %v", t.ID, scheduleErr)
				continue
			}
			if !inside {
				if err := s.m.ConvertProxyPauseToSchedule(t.ID); err != nil {
					log.Printf("[proxy] schedule pause task %s: %v", t.ID, err)
				}
				continue
			}
		}
		var err error
		if s.engine != nil {
			_, err = s.admitTaskWhenScheduledWithReason(t, "resume", true, false, "paused_by_proxy", true)
			if afterAdmission != nil {
				afterAdmission()
			}
		} else {
			err = s.m.ClearPauseReason(t.ID)
		}
		if err != nil {
			// Admission re-checks the persisted reason under concMu. A user pause,
			// terminal transition, delete, or archive therefore cannot be revived;
			// only keep probing if the proxy pause is still eligible.
			st := t.lifecycleSnapshot()
			if st.PauseReason == "paused_by_proxy" && !isTerminalStatus(st.Status) {
				t.updateLifecycle(func(v *taskLifecycleState) {
					v.ReconnectMax = max
					v.ReconnectNextAt = time.Now().Add(interval).Unix()
				})
			}
			log.Printf("[proxy] resume task %s: %v", t.ID, err)
			continue
		}
		// The non-queued path's startAdmittedTask already resumes the Engine
		// while concMu is held. Do not Resume again after releasing the lock:
		// a concurrent user/orchestrator pause could have installed a newer
		// barrier in the meantime.
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
