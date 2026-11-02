package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	settingProxyReconnectMax      = "proxy_reconnect_max_attempts"
	settingProxyReconnectInterval = "proxy_reconnect_interval_seconds"
	defaultProxyReconnectMax      = 5
	defaultProxyReconnectInterval = 30
)

type proxyReconnectGuard struct {
	generation   uint64
	attempt      int
	checkAttempt bool
}

func (s *Server) snapshotProxyReconnectGuards(checkAttempt bool) map[string]proxyReconnectGuard {
	guards := make(map[string]proxyReconnectGuard)
	if s == nil || s.m == nil {
		return guards
	}
	for _, t := range s.m.List() {
		st := t.lifecycleSnapshot()
		if st.PauseReason != "paused_by_proxy" {
			continue
		}
		guards[t.ID] = proxyReconnectGuard{
			generation:   st.ReconnectGeneration,
			attempt:      st.ReconnectAttempt,
			checkAttempt: checkAttempt,
		}
	}
	return guards
}

func proxyReconnectGuardMatches(st taskLifecycleState, guard proxyReconnectGuard) bool {
	return st.PauseReason == "paused_by_proxy" && st.ReconnectGeneration == guard.generation &&
		(!guard.checkAttempt || st.ReconnectAttempt == guard.attempt)
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

func (s *Server) runProxyProbe(ctx context.Context, proxyRaw, caPEMPath string, probes []string, timeout time.Duration) egressProbeResult {
	if s != nil && s.proxyProbe != nil {
		return s.proxyProbe(ctx, proxyRaw, caPEMPath, probes, timeout)
	}
	return probeEgress(ctx, proxyRaw, caPEMPath, probes, timeout)
}

func (s *Server) proxyReconnectSuccess() {
	s.proxyReconnectSuccessWithGuards(s.snapshotProxyReconnectGuards(false), nil)
}

// The hook lets recovery tests place a competing pause after admission.
func (s *Server) proxyReconnectSuccessWithHook(afterAdmission func()) {
	s.proxyReconnectSuccessWithGuards(s.snapshotProxyReconnectGuards(false), afterAdmission)
}

func (s *Server) proxyReconnectSuccessWithGuards(guards map[string]proxyReconnectGuard, afterAdmission func()) {
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	for _, t := range s.m.List() {
		guard, ok := guards[t.ID]
		if !ok {
			continue
		}
		if !proxyReconnectGuardMatches(t.lifecycleSnapshot(), guard) {
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
			_, err = s.admitTaskWhenScheduledWithGenerationChecked(t, "resume", true, false, "paused_by_proxy", true, guard.generation, true)
			if afterAdmission != nil {
				afterAdmission()
			}
		} else {
			err = s.m.ClearPauseReason(t.ID)
		}
		if err != nil {
			log.Printf("[proxy] resume task %s: %v", t.ID, err)
			continue
		}
		// The non-queued path's startAdmittedTask already resumes the Engine
		// while concMu is held. Do not Resume again after releasing the lock:
		// a concurrent user/orchestrator pause could have installed a newer
		// barrier in the meantime.
		t.updateLifecycle(func(v *taskLifecycleState) {
			if !proxyReconnectGuardMatches(*v, guard) {
				return
			}
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
	guards := s.snapshotProxyReconnectGuards(false)
	if proxy == "" {
		writeJSON(w, 200, map[string]any{"ok": true, "direct": true})
		s.proxyReconnectSuccessWithGuards(guards, nil)
		return
	}
	res := s.runProxyProbe(r.Context(), proxy, "", defaultEgressProbes, 10*time.Second)
	if res.OK {
		s.proxyReconnectSuccessWithGuards(guards, nil)
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
