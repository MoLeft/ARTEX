package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
)

func proxyTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestProxyRequestFailureNeverPausesTasks(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	m, err := NewManager(t.TempDir(), addr)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	if m.traffic == nil {
		t.Fatal("recording proxy unavailable")
	}
	m.globalProxy = "http://127.0.0.1:1"
	m.trafficOn = true
	if err := m.egress.SetUpstream(m.globalProxy, false); err != nil {
		t.Fatal(err)
	}
	if err := m.traffic.SetUpstreamProxy(m.egress.Addr()); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("request failure", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	proxyURL, _ := url.Parse(m.traffic.ProxyAddr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 2 * time.Second}
	_, _ = client.Get("https://example.invalid/")
	if st := task.lifecycleSnapshot(); st.PauseReason == "paused_by_proxy" || st.Paused {
		t.Fatalf("one request failure paused task: %+v", st)
	}
}

func TestLegacyProxyPauseStartupRecovery(t *testing.T) {
	m := proxyTestManager(t)
	task, err := m.CreateTask("legacy proxy pause", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	if err := m.ApplyProxyPause(task.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	if st := task.lifecycleSnapshot(); st.PauseReason == "paused_by_proxy" || st.Paused {
		t.Fatalf("legacy task remained proxy-paused after startup: %+v", st)
	}
}

func TestManualProxyTestNeverFallsBack(t *testing.T) {
	m := proxyTestManager(t)
	task, err := m.CreateTask("manual proxy test", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	m.globalProxy = "http://127.0.0.1:1"
	if err := m.SetProxyDirectRetry(true); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	s.proxyProbe = func(_ context.Context, raw, _ string, _ []string, _ time.Duration) egressProbeResult {
		if raw != m.globalProxy {
			t.Fatalf("manual probe used fallback route %q", raw)
		}
		return egressProbeResult{Error: "proxy refused"}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/test-proxy", nil)
	r.SetPathValue("id", task.ID)
	w := httptest.NewRecorder()
	s.testTaskProxy(w, r)
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.OK {
		t.Fatalf("manual probe result=%s error=%v", w.Body.String(), err)
	}
}

func TestProxyPauseCauseIsDistinct(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(agent.AbortPausedByProxy)
	code, short, _, ok := agent.AbortReason(ctx)
	if !ok || code != "paused_by_proxy" || short == "" {
		t.Fatalf("proxy cause = %q/%q/%v", code, short, ok)
	}
}

func TestProxyReconnectReasonGate(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   bool
	}{
		{"paused_by_proxy", true},
		{"paused_by_user", false},
		{"paused_by_schedule", false},
		{"paused_by_orchestrator", false},
	} {
		got := tc.reason == "paused_by_proxy"
		if got != tc.want {
			t.Errorf("reason %q gate=%v, want %v", tc.reason, got, tc.want)
		}
	}
}

func TestProxyAdmissionRechecksReasonAfterSnapshot(t *testing.T) {
	m := proxyTestManager(t)
	task, err := m.CreateTask("proxy gate", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	if err := m.ApplyProxyPause(task.ID); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	s.engine.Pause(task.ID, agent.AbortPausedByProxy)
	// Simulate a user re-pause winning after proxyReconnectSuccess took its
	// snapshot but before the admission lock was acquired.
	if _, err := m.pg.Exec(`UPDATE tasks SET pause_reason='paused_by_user' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	task.updateLifecycle(func(v *taskLifecycleState) { v.PauseReason = "paused_by_user" })
	if _, err := s.admitTaskWhenScheduledWithReason(task, "resume", true, false, "paused_by_proxy", true); err == nil {
		t.Fatal("proxy admission passed after user changed pause reason")
	}
	if !s.engine.IsPaused(task.ID) {
		t.Fatal("user re-pause barrier was cleared")
	}
}

func TestProxyQueuedAdmissionDoesNotResumeEngine(t *testing.T) {
	m := proxyTestManager(t)
	if err := m.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}
	holder, err := m.CreateTask("holder", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	proxyTask, err := m.CreateTask("queued proxy", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(holder.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(proxyTask.ID, DeleteTaskOptions{})
	}()
	if err := m.ApplyProxyPause(proxyTask.ID); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	s.engine.started.Store(holder.ID, true)
	s.engine.Pause(proxyTask.ID, agent.AbortPausedByProxy)
	queued, err := s.admitTaskWhenScheduledWithReason(proxyTask, "resume", true, false, "paused_by_proxy", true)
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("proxy task was expected to queue behind the running holder")
	}
	if !s.engine.IsPaused(proxyTask.ID) {
		t.Fatal("queued proxy task was resumed")
	}
}

func TestTerminalStatusClearsProxyReconnectState(t *testing.T) {
	m := proxyTestManager(t)
	task, err := m.CreateTask("terminal proxy", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	if err := m.ApplyProxyPause(task.ID); err != nil {
		t.Fatal(err)
	}
	task.updateLifecycle(func(v *taskLifecycleState) {
		v.ReconnectAttempt, v.ReconnectMax, v.ReconnectNextAt = 2, 5, 123
		v.ReconnectExhausted, v.ReconnectLastError = true, "proxy down"
	})
	if err := m.SetTaskStatus(task.ID, "timeout"); err != nil {
		t.Fatal(err)
	}
	stored, err := m.pg.GetTask(mustTaskID(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored.PauseReason != "" {
		t.Fatalf("terminal row retained pause reason %q", stored.PauseReason)
	}
	st := task.lifecycleSnapshot()
	if st.PauseReason != "" || st.ReconnectAttempt != 0 || st.ReconnectNextAt != 0 || st.ReconnectExhausted {
		t.Fatalf("terminal lifecycle retained reconnect state: %+v", st)
	}
}

func TestProxyAdmissionPreservesCalendarOverride(t *testing.T) {
	m := proxyTestManager(t)
	task, err := m.CreateTask("calendar proxy", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	if _, err := m.pg.Exec(`UPDATE tasks SET schedule_paused=true, schedule_manual_until='2099-01-01 00:00:00+00' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyProxyPause(task.ID); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	s.engine.Pause(task.ID, agent.AbortPausedByProxy)
	if queued, err := s.admitTaskWhenScheduledWithReason(task, "resume", true, false, "paused_by_proxy", true); err != nil || queued {
		t.Fatalf("proxy admission queued=%v err=%v", queued, err)
	}
	var schedulePaused bool
	var manualUntil time.Time
	if err := m.pg.QueryRow(`SELECT schedule_paused, schedule_manual_until FROM tasks WHERE id=$1`, task.ID).Scan(&schedulePaused, &manualUntil); err != nil {
		t.Fatal(err)
	}
	if !schedulePaused || !manualUntil.Equal(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("proxy admission changed calendar state: paused=%v until=%v", schedulePaused, manualUntil)
	}
}

func TestProxyReconnectSuccessKeepsCompetingUserPauseBarrier(t *testing.T) {
	m := proxyTestManager(t)
	task, err := m.CreateTask("proxy success race", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	s := newAdmissionTestServer(m, nil)
	m.SetTaskControlMutex(&s.concMu)
	m.globalProxy = "http://proxy.invalid"
	if _, err := m.ApplyProxyPauseWithCallback(task.ID, func() { s.engine.Pause(task.ID, agent.AbortPausedByProxy) }); err != nil {
		t.Fatal(err)
	}

	var pauseErr error
	s.proxyReconnectSuccessWithHook(func() {
		_, pauseErr = s.applyTaskControl(task, "pause")
	})
	if pauseErr != nil {
		t.Fatalf("competing user pause failed: %v", pauseErr)
	}
	state := task.lifecycleSnapshot()
	if state.PauseReason != "paused_by_user" || !state.Paused {
		t.Fatalf("user pause was overwritten: %+v", state)
	}
	if !s.engine.IsPaused(task.ID) {
		t.Fatal("user pause barrier was cleared by a post-admission Resume")
	}
}

func TestProxyReconnectManualSuccessDoesNotAdmitNewRound(t *testing.T) {
	m := proxyTestManager(t)
	task, err := m.CreateTask("proxy stale manual success", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	s := newAdmissionTestServer(m, nil)
	m.SetTaskControlMutex(&s.concMu)
	m.globalProxy = "http://proxy.invalid"
	if err := m.ApplyProxyPause(task.ID); err != nil {
		t.Fatal(err)
	}
	s.engine.Pause(task.ID, agent.AbortPausedByProxy)
	guards := s.snapshotProxyReconnectGuards(false)
	task.updateLifecycle(func(v *taskLifecycleState) {
		v.ReconnectGeneration++
		v.ReconnectNextAt = 987654
		v.ReconnectLastError = "new round still failing"
	})
	before := task.lifecycleSnapshot()
	s.proxyReconnectSuccessWithGuards(guards, nil)
	after := task.lifecycleSnapshot()
	if after.PauseReason != "paused_by_proxy" || !s.engine.IsPaused(task.ID) {
		t.Fatalf("stale manual success admitted new round: %+v", after)
	}
	if after.ReconnectGeneration != before.ReconnectGeneration || after.ReconnectNextAt != before.ReconnectNextAt || after.ReconnectLastError != before.ReconnectLastError {
		t.Fatalf("stale manual success wiped new round state: before=%+v after=%+v", before, after)
	}
}

func TestProxyReconnectSettingsSuccessGuardsGenerationZero(t *testing.T) {
	m := proxyTestManager(t)
	task, err := m.CreateTask("proxy generation zero", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	s := newAdmissionTestServer(m, nil)
	m.SetTaskControlMutex(&s.concMu)
	m.globalProxy = "http://proxy.invalid"
	if err := m.ApplyProxyPause(task.ID); err != nil {
		t.Fatal(err)
	}
	s.engine.Pause(task.ID, agent.AbortPausedByProxy)
	initial := task.lifecycleSnapshot()
	if initial.ReconnectGeneration != 0 {
		t.Fatalf("fresh persisted proxy pause generation=%d, want zero", initial.ReconnectGeneration)
	}
	guards := s.snapshotProxyReconnectGuards(false)
	task.updateLifecycle(func(v *taskLifecycleState) {
		v.ReconnectGeneration = 1
		v.ReconnectNextAt = 13579
		v.ReconnectLastError = "generation one failure"
	})
	before := task.lifecycleSnapshot()
	s.proxyReconnectSuccessWithGuards(guards, nil)
	after := task.lifecycleSnapshot()
	if after.PauseReason != "paused_by_proxy" || !s.engine.IsPaused(task.ID) {
		t.Fatalf("generation-zero stale settings success admitted new round: %+v", after)
	}
	if after.ReconnectGeneration != before.ReconnectGeneration || after.ReconnectNextAt != before.ReconnectNextAt || after.ReconnectLastError != before.ReconnectLastError {
		t.Fatalf("generation-zero stale settings success wiped state: before=%+v after=%+v", before, after)
	}
}
