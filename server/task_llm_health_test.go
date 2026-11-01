package server

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

// classifyPlannerFailure is the gate that decides whether a failed planning
// round is worth another request at all. Getting this wrong is what made the
// planner re-request an exhausted MiniMax key every ten minutes.
func TestClassifyPlannerFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "minimax token plan 2056",
			msg:  `anthropic: status 429: {"type":"error","error":{"type":"rate_limit_error","message":"已达到 Token Plan 用量上限：请升级 Token Plan 套餐或购买积分补充用量。(2056)"}}`,
			want: db.LLMFailurePermanent,
		},
		{name: "openai insufficient_quota", msg: `openai: status 402: insufficient_quota`, want: db.LLMFailurePermanent},
		{name: "credit balance", msg: `anthropic: status 402: credit balance is too low`, want: db.LLMFailurePermanent},
		{name: "rejected key", msg: `openai: status 401: invalid api key`, want: db.LLMFailurePermanent},
		{name: "bad request", msg: `openai: status 400: bad request`, want: db.LLMFailurePermanent},
		{name: "context too long", msg: `anthropic: status 400: context length exceeded`, want: db.LLMFailurePermanent},

		// Throttles and outages clear on their own; these must keep retrying.
		{name: "plain throttle", msg: `openai: status 429: rate limit exceeded`, want: db.LLMFailureTransient},
		{name: "minimax per-minute 2062", msg: `anthropic: status 429: {"message":"已达到 Token Plan 速率限制。(2062)"}`, want: db.LLMFailureTransient},
		{name: "server error", msg: `openai: status 500: internal server error`, want: db.LLMFailureTransient},
		{name: "network", msg: `dial tcp: network is unreachable`, want: db.LLMFailureTransient},
		{name: "unclassified", msg: `something odd happened`, want: db.LLMFailureTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := classifyPlannerFailure(errors.New(tc.msg))
			if got != tc.want {
				t.Fatalf("classify(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// The backoff ladder must actually grow, otherwise a "retry" is just the old
// fixed ten-minute heartbeat wearing a new name.
func TestPlannerRetryBackoffDoublesAndCaps(t *testing.T) {
	t.Parallel()
	var p db.LLMRetryPolicy
	want := []time.Duration{
		time.Minute,      // 1
		2 * time.Minute,  // 2
		4 * time.Minute,  // 3
		8 * time.Minute,  // 4
		16 * time.Minute, // 5
		30 * time.Minute, // 6 → capped
		30 * time.Minute, // 7 → still capped
	}
	for i, expect := range want {
		if got := p.PlannerRetryBackoff(i + 1); got != expect {
			t.Fatalf("backoff(%d) = %v, want %v", i+1, got, expect)
		}
	}
	// A configured interval replaces the first step but keeps doubling.
	fixed := db.LLMRetryPolicy{Planner: db.RetryRule{IntervalMS: 5000}}
	if got := fixed.PlannerRetryBackoff(1); got != 5*time.Second {
		t.Fatalf("configured base = %v, want 5s", got)
	}
	if got := fixed.PlannerRetryBackoff(2); got != 10*time.Second {
		t.Fatalf("second step = %v, want 10s", got)
	}
}

func TestPlannerRetryAttemptsDefaultAndDisable(t *testing.T) {
	t.Parallel()
	var zero db.LLMRetryPolicy
	if got := zero.PlannerRetryAttempts(); got != db.DefaultPlannerRetryAttempts {
		t.Fatalf("default attempts = %d, want %d", got, db.DefaultPlannerRetryAttempts)
	}
	off := db.LLMRetryPolicy{Planner: db.RetryRule{Attempts: -1}}
	if got := off.PlannerRetryAttempts(); got != -1 {
		t.Fatalf("disabled attempts = %d, want -1", got)
	}
}

// Persistence is the whole point of layer 6: a restart must not reset the count
// and burn the same budget again.
func TestPlannerFailureStateSurvivesRestart(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()

	task, err := m.CreateTask("llm health", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.DeleteTask(n) }()

	for i := 1; i <= 3; i++ {
		h, recErr := m.pg.RecordLLMFailure(n, db.LLMFailureTransient)
		if recErr != nil {
			t.Fatal(recErr)
		}
		if h.Failures != i {
			t.Fatalf("failure count = %d, want %d", h.Failures, i)
		}
	}
	at := time.Now().Add(7 * time.Minute).Truncate(time.Second)
	if err := m.pg.SetLLMNextAttempt(n, &at); err != nil {
		t.Fatal(err)
	}

	// "Restart": read the state back from PG, exactly what a new process would do.
	h, err := m.pg.LLMHealth(n)
	if err != nil {
		t.Fatal(err)
	}
	if h.Failures != 3 {
		t.Fatalf("failure count lost across restart: %d", h.Failures)
	}
	if h.NextAttempt == nil || !h.NextAttempt.Equal(at) {
		t.Fatalf("next attempt lost across restart: %v", h.NextAttempt)
	}

	// Blocking is idempotent and keeps the first reason.
	if err := m.pg.BlockTaskOnLLM(n, "模型额度已用尽"); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.BlockTaskOnLLM(n, "第二次不该覆盖"); err != nil {
		t.Fatal(err)
	}
	h, err = m.pg.LLMHealth(n)
	if err != nil {
		t.Fatal(err)
	}
	if !h.IsBlocked() {
		t.Fatal("task should be blocked")
	}
	if h.Blocked != "模型额度已用尽" {
		t.Fatalf("block reason overwritten: %q", h.Blocked)
	}
	if h.NextAttempt != nil {
		t.Fatal("blocking must clear the pending retry time")
	}

	// Clearing (user resume) wipes everything.
	if err := m.pg.ClearLLMFailure(n); err != nil {
		t.Fatal(err)
	}
	h, err = m.pg.LLMHealth(n)
	if err != nil {
		t.Fatal(err)
	}
	if h.Failures != 0 || h.IsBlocked() || h.NextAttempt != nil {
		t.Fatalf("state not cleared: %+v", h)
	}
}

// The planner gate is what stops the periodic request. It must read the
// persisted state, so a task resumed from disk is still gated.
func TestPlannerGatedByPersistedFailure(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	s := New(ctx, m, dir, dir, dir)

	task, err := m.CreateTask("gated", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.DeleteTask(n) }()

	if s.engine.plannerLLMGated(task.ID) {
		t.Fatal("a healthy task must not be gated")
	}

	future := time.Now().Add(time.Hour)
	if err := m.pg.SetLLMNextAttempt(n, &future); err != nil {
		t.Fatal(err)
	}
	if !s.engine.plannerLLMGated(task.ID) {
		t.Fatal("a pending retry time must gate the planner")
	}

	past := time.Now().Add(-time.Second)
	if err := m.pg.SetLLMNextAttempt(n, &past); err != nil {
		t.Fatal(err)
	}
	if s.engine.plannerLLMGated(task.ID) {
		t.Fatal("an elapsed retry time must let the planner through")
	}

	if err := m.pg.BlockTaskOnLLM(n, "quota"); err != nil {
		t.Fatal(err)
	}
	if !s.engine.plannerLLMGated(task.ID) {
		t.Fatal("a blocked task must never plan again on its own")
	}

	// User resume clears the block, so the gate opens again.
	if err := m.SetTaskPaused(task.ID, false); err != nil {
		t.Fatal(err)
	}
	if s.engine.plannerLLMGated(task.ID) {
		t.Fatal("resuming the task must clear the planner gate")
	}
}

// A quota failure must stop retries on the first round, not after the cap.
func TestRecordPlannerFailureStopsImmediatelyOnQuota(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	s := New(ctx, m, dir, dir, dir)

	task, err := m.CreateTask("quota stop", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.DeleteTask(n) }()

	quota := errors.New(`anthropic: status 429: {"message":"已达到 Token Plan 用量上限：请升级 Token Plan 套餐或购买积分补充用量。(2056)"}`)
	s.engine.recordPlannerFailure(task, quota)

	h, err := m.pg.LLMHealth(n)
	if err != nil {
		t.Fatal(err)
	}
	if !h.IsBlocked() {
		t.Fatal("a quota failure must block on the first round")
	}
	if h.Failures != 1 {
		t.Fatalf("failure count = %d, want 1 — the count should record the failure, the block should stop the retry", h.Failures)
	}
	if h.NextAttempt != nil {
		t.Fatal("a blocked task must not carry a pending retry time")
	}
}

// Transient failures back off first and only block once the cap is reached.
func TestRecordPlannerFailureBacksOffThenBlocks(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	s := New(ctx, m, dir, dir, dir)

	task, err := m.CreateTask("transient backoff", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.DeleteTask(n) }()

	limit := s.engine.plannerRetryAttempts()
	if limit <= 0 {
		t.Fatalf("test needs a finite cap, got %d", limit)
	}
	blip := errors.New("openai: status 500: internal server error")

	for i := 1; i < limit; i++ {
		s.engine.recordPlannerFailure(task, blip)
		h, err := m.pg.LLMHealth(n)
		if err != nil {
			t.Fatal(err)
		}
		if h.IsBlocked() {
			t.Fatalf("round %d blocked before the cap of %d", i, limit)
		}
		if h.Failures != i {
			t.Fatalf("round %d: failures = %d", i, h.Failures)
		}
		if h.NextAttempt == nil || !h.NextAttempt.After(time.Now()) {
			t.Fatalf("round %d: no future retry time", i)
		}
	}

	// The round that reaches the cap stops instead of scheduling another.
	s.engine.recordPlannerFailure(task, blip)
	h, err := m.pg.LLMHealth(n)
	if err != nil {
		t.Fatal(err)
	}
	if !h.IsBlocked() {
		t.Fatalf("round %d must block at the cap", limit)
	}
	if h.NextAttempt != nil {
		t.Fatal("the blocking round must not leave a retry time behind")
	}
}

// A blocked task is not user-paused, but "continue" is still its recovery
// action. Before this wiring, resume answered "仅已暂停的任务可以继续", so the
// banner's retry button could never work.
func TestResumeClearsLLMBlock(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	s := New(ctx, m, dir, dir, dir)

	task, err := m.CreateTask("resume blocked", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.DeleteTask(n) }()

	s.engine.recordPlannerFailure(task, errors.New("openai: status 402: insufficient_quota"))
	h, err := m.pg.LLMHealth(n)
	if err != nil {
		t.Fatal(err)
	}
	if !h.IsBlocked() {
		t.Fatal("setup: task should be blocked")
	}
	if !s.engine.plannerLLMGated(task.ID) {
		t.Fatal("setup: planner should be gated")
	}

	if _, err := s.applyTaskControl(task, "resume"); err != nil {
		t.Fatalf("resume on a blocked task must be allowed: %v", err)
	}

	h, err = m.pg.LLMHealth(n)
	if err != nil {
		t.Fatal(err)
	}
	if h.IsBlocked() || h.Failures != 0 || h.NextAttempt != nil {
		t.Fatalf("resume did not clear the LLM block: %+v", h)
	}
	if s.engine.plannerLLMGated(task.ID) {
		t.Fatal("resume must reopen the planner gate")
	}
}
