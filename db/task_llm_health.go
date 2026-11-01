package db

import (
	"strings"
	"time"
)

// 任务级 LLM 失败的持久化状态（LLM 重试第 6 层，调度侧见 server/task_llm_health.go）。
//
// 前五层都在一次模型调用内部：建连重试、空响应重试、安全窗口重放、轮询熔断、
// 意图重跑。它们管不了「这次调用失败之后，planner 心跳还要不要再发起一次」——
// 那是另一个时间尺度。没有这一层时，planner 每 10 分钟原样醒一次，把已经欠费的
// 配置再请求一遍，日志里每次都是同一个 2056，任务既不推进也不停止。
//
// 所以这里落的是「还能不能再试」：
//   - 额度耗尽：额度不会自己回来，不重试，直接停止，等用户修配置；
//   - 瞬时失败：指数退避，连续超过上限才停止。
//
// 全部落库。只放内存的话，重启后计数归零，等于把已经烧掉的次数再烧一遍。

// LLM failure kinds, as persisted in tasks.llm_failure_kind.
const (
	// LLMFailurePermanent means retrying cannot change the outcome: the account is
	// out of allowance or credit, the key is rejected, or the request itself is
	// invalid. The task stops immediately and waits for a human.
	LLMFailurePermanent = "permanent"
	// LLMFailureTransient means a throttle, 5xx, network or protocol failure that
	// may clear on its own. Backed off, then stopped at the configured cap.
	LLMFailureTransient = "transient"
)

const (
	// DefaultPlannerRetryAttempts matches the built-in planner layer default.
	DefaultPlannerRetryAttempts = 5
	// DefaultPlannerRetryInterval is the first backoff step; later steps double.
	DefaultPlannerRetryInterval = time.Minute
	// maxPlannerRetryInterval caps the exponential ladder so a long outage does not
	// park the task for hours.
	maxPlannerRetryInterval = 30 * time.Minute
)

// TaskLLMHealth is the persisted planner failure state of one task.
type TaskLLMHealth struct {
	Failures    int        // consecutive planner failures; reset by any success
	Kind        string     // LLMFailureQuota | LLMFailureTransient | ""
	NextAttempt *time.Time // nil = no backoff pending
	Blocked     string     // non-empty = auto retry stopped, needs a human
}

// IsBlocked reports whether auto retry has been stopped for this task.
func (h TaskLLMHealth) IsBlocked() bool { return strings.TrimSpace(h.Blocked) != "" }

// PlannerRetryAttempts resolves the layer-6 attempt cap. -1 disables the cap
// (back off forever), 0 keeps the default.
func (p LLMRetryPolicy) PlannerRetryAttempts() int {
	switch n := p.Planner.Attempts; {
	case n < 0:
		return -1
	case n == 0:
		return DefaultPlannerRetryAttempts
	default:
		return n
	}
}

// PlannerRetryBackoff returns the wait before attempt number failures+1. The
// first step is the configured interval (default 1 minute) and every further
// step doubles, capped at maxPlannerRetryInterval.
func (p LLMRetryPolicy) PlannerRetryBackoff(failures int) time.Duration {
	base := p.Planner.Interval()
	if base <= 0 {
		base = DefaultPlannerRetryInterval
	}
	if failures < 1 {
		failures = 1
	}
	d := base
	for i := 1; i < failures && d < maxPlannerRetryInterval; i++ {
		d *= 2
	}
	return min(d, maxPlannerRetryInterval)
}

// LLMHealth reads the persisted planner failure state for one task.
func (d *DB) LLMHealth(taskID int64) (TaskLLMHealth, error) {
	var (
		h    TaskLLMHealth
		next *time.Time
	)
	err := d.QueryRow(`SELECT llm_failure_count, COALESCE(llm_failure_kind,''),
llm_next_attempt_at, COALESCE(llm_blocked_reason,'') FROM tasks WHERE id=$1`, taskID).
		Scan(&h.Failures, &h.Kind, &next, &h.Blocked)
	h.NextAttempt = next
	return h, err
}

// RecordLLMFailure atomically bumps the consecutive-failure counter and returns
// the post-increment state, so two concurrent planner rounds cannot both read
// the same count. kind is LLMFailureQuota or LLMFailureTransient.
func (d *DB) RecordLLMFailure(taskID int64, kind string) (TaskLLMHealth, error) {
	var (
		h    TaskLLMHealth
		next *time.Time
	)
	err := d.QueryRow(`UPDATE tasks
SET llm_failure_count = llm_failure_count + 1,
    llm_failure_kind  = $2,
    llm_blocked_reason = ''
WHERE id=$1
RETURNING llm_failure_count, COALESCE(llm_failure_kind,''), llm_next_attempt_at, COALESCE(llm_blocked_reason,'')`,
		taskID, kind).Scan(&h.Failures, &h.Kind, &next, &h.Blocked)
	h.NextAttempt = next
	return h, err
}

// SetLLMNextAttempt schedules (or clears, when at is nil) the next planner wakeup.
func (d *DB) SetLLMNextAttempt(taskID int64, at *time.Time) error {
	_, err := d.Exec(`UPDATE tasks SET llm_next_attempt_at=$2 WHERE id=$1`, taskID, at)
	return err
}

// BlockTaskOnLLM stops automatic retries and records why. Idempotent: a second
// call keeps the first reason, since that is the one that actually fired.
func (d *DB) BlockTaskOnLLM(taskID int64, reason string) error {
	_, err := d.Exec(`UPDATE tasks
SET llm_blocked_reason = CASE WHEN COALESCE(llm_blocked_reason,'')='' THEN $2 ELSE llm_blocked_reason END,
    llm_next_attempt_at = NULL
WHERE id=$1`, taskID, strings.TrimSpace(reason))
	return err
}

// ClearLLMFailure resets the counter on any successful planner round, so the
// cap only ever counts genuine consecutive failures.
func (d *DB) ClearLLMFailure(taskID int64) error {
	_, err := d.Exec(`UPDATE tasks
SET llm_failure_count=0, llm_failure_kind='', llm_next_attempt_at=NULL, llm_blocked_reason=''
WHERE id=$1 AND (llm_failure_count<>0 OR COALESCE(llm_failure_kind,'')<>'' OR llm_next_attempt_at IS NOT NULL OR COALESCE(llm_blocked_reason,'')<>'')`, taskID)
	return err
}
