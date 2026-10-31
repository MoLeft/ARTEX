package server

import (
	"context"
	"errors"
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

func TestProxyFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"proxy connect", errors.New("proxyconnect tcp: connection refused"), true},
		{"reset", errors.New("dial tcp: connection reset by peer"), false},
		{"eof", errors.New("unexpected EOF"), false},
		{"quota", errors.New("status 429: insufficient_quota"), false},
		{"auth", errors.New("status 401: invalid api key"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isProxyFailure(tc.err); got != tc.want {
				t.Fatalf("isProxyFailure(%v)=%v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestGlobalProxyTrafficFailureClassification(t *testing.T) {
	if !isGlobalProxyDialFailure(errors.New("dial tcp 127.0.0.1:8080: connect: connection refused")) {
		t.Fatal("upstream dial refusal should be eligible for probe confirmation")
	}
	if isGlobalProxyDialFailure(errors.New("status 404: target not found")) {
		t.Fatal("ordinary target failure must not be treated as proxy failure")
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
