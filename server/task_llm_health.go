package server

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// LLM 重试第 6 层：planner 调度级退避与停止。见 db/task_llm_health.go 的说明。
//
// 前五层都在一次模型调用内部，管不到「这次失败之后，10 分钟后的心跳还要不要再发
// 一次请求」。没有这一层时，planner 心跳是纯粹的定时器：每 10 分钟醒一次，把已经
// 欠费的配置原样再请求一遍，任务既不推进也不停止。
//
// 这一层只做两件事：
//   - 给不可恢复的失败（额度耗尽、密钥失效、请求非法）立即停机；
//   - 给可能自愈的失败一个指数退避，超过上限后同样停机。
//
// 计数与下次时间都落库，所以重启不会把已经烧掉的次数清零。

// taskLLMBlocked reports whether layer 6 stopped this task's automatic retries.
// Such a task is not user-paused but is equally stopped, so "continue" is its
// recovery action too.
func taskLLMBlocked(t *Task) bool {
	return t != nil && strings.TrimSpace(t.llmStateSnapshot().BlockedReason) != ""
}

// clearTaskLLMBlock wipes the layer-6 state for one task and mirrors it into the
// live copy. Called when the user explicitly resumes/retries: pressing continue
// is the operator asserting the model config is fixed, and leaving the gate shut
// would make the task look recovered while never planning again.
func (s *Server) clearTaskLLMBlock(t *Task) error {
	if t == nil || s.m == nil || s.m.pg == nil {
		return nil
	}
	if st := t.llmStateSnapshot(); st.BlockedReason == "" && st.FailureCount == 0 && st.NextAttemptAt == 0 {
		return nil
	}
	n, err := parseTaskID(t.ID)
	if err != nil {
		return err
	}
	if err := s.m.pg.ClearLLMFailure(n); err != nil {
		return err
	}
	t.setLLMHealth(0, "", 0, "")
	return nil
}

// classifyPlannerFailure maps a planner error onto the two persisted kinds plus
// a human-readable reason. The default is transient: an unclassified failure is
// more likely a blip than a permanent misconfiguration, and backing off is the
// cheaper mistake.
func classifyPlannerFailure(err error) (kind, reason string) {
	if err == nil {
		return "", ""
	}
	reason = err.Error()
	// Quota/credit exhaustion: the account cannot call the model until someone
	// tops up or upgrades. This is the MiniMax 2056 case.
	if isQuotaExhaustedError(err) {
		return db.LLMFailurePermanent, reason
	}
	// Deterministic rejections: a bad or over-long request, a revoked key, a
	// missing model. Replaying it on another profile fails identically, so there
	// is nothing to wait out.
	s := strings.ToLower(reason)
	for _, marker := range []string{
		"status 400", "status 401", "status 403", "status 404", "status 422",
		"context length", "context_length", "maximum context", "too long",
		"invalid api key", "invalid_api_key", "unauthorized", "model not found",
	} {
		if strings.Contains(s, marker) {
			return db.LLMFailurePermanent, reason
		}
	}
	return db.LLMFailureTransient, reason
}

// plannerRetryAttempts resolves the layer-6 cap from the global retry policy.
// -1 disables the cap; 0 keeps the built-in default.
func (e *Engine) plannerRetryAttempts() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return db.DefaultPlannerRetryAttempts
	}
	return e.m.pg.LLMRetryPolicy().PlannerRetryAttempts()
}

// plannerRetryBackoff resolves the wait before the next attempt.
func (e *Engine) plannerRetryBackoff(failures int) time.Duration {
	if e == nil || e.m == nil || e.m.pg == nil {
		return db.DefaultPlannerRetryInterval
	}
	return e.m.pg.LLMRetryPolicy().PlannerRetryBackoff(failures)
}

// plannerLLMGated reports whether this task must not start another planning
// round right now. Read straight from PG (once per heartbeat at most) so a
// restart picks the state back up instead of starting from zero.
func (e *Engine) plannerLLMGated(taskID string) bool {
	n, err := parseTaskID(taskID)
	if err != nil || e.m == nil || e.m.pg == nil {
		return false // cannot tell → behave as before rather than wedge the task
	}
	h, err := e.m.pg.LLMHealth(n)
	if err != nil {
		return false
	}
	if h.IsBlocked() {
		return true
	}
	return h.NextAttempt != nil && time.Now().Before(*h.NextAttempt)
}

// plannerNextWake returns how long the heartbeat should wait. When a backoff is
// pending and shorter than the normal interval, wake exactly then — otherwise a
// one-minute backoff would silently become ten.
func (e *Engine) plannerNextWake(taskID string, normal time.Duration) time.Duration {
	n, err := parseTaskID(taskID)
	if err != nil || e.m == nil || e.m.pg == nil {
		return normal
	}
	h, err := e.m.pg.LLMHealth(n)
	if err != nil || h.NextAttempt == nil {
		return normal
	}
	if d := time.Until(*h.NextAttempt); d > 0 && d < normal {
		return d + time.Second
	}
	return normal
}

// recordPlannerFailure applies the layer-6 policy to one failed planning round.
// It never returns an error: failing to persist the state must not take the
// task down a second path, and the next heartbeat will try again.
func (e *Engine) recordPlannerFailure(t *Task, err error) {
	if t == nil || e.m == nil || e.m.pg == nil {
		return
	}
	n, parseErr := parseTaskID(t.ID)
	if parseErr != nil {
		return
	}
	kind, reason := classifyPlannerFailure(err)

	h, recErr := e.m.pg.RecordLLMFailure(n, kind)
	if recErr != nil {
		log.Printf("[planner] task %s 记录失败次数失败: %v", t.ID, recErr)
		return
	}

	// 不可恢复：不排队重试，直接停。额度耗尽属于这一类——额度不会自己回来。
	if kind == db.LLMFailurePermanent {
		e.stopPlannerRetries(t, n, h, kind, reason, "模型不可用，已停止自动重试：配置恢复后请手动继续")
		return
	}

	if limit := e.plannerRetryAttempts(); limit >= 0 && h.Failures >= limit {
		e.stopPlannerRetries(t, n, h, kind, reason,
			fmt.Sprintf("连续 %d 次模型错误，已停止自动重试：配置恢复后请手动继续", h.Failures))
		return
	}

	backoff := e.plannerRetryBackoff(h.Failures)
	at := time.Now().Add(backoff)
	if setErr := e.m.pg.SetLLMNextAttempt(n, &at); setErr != nil {
		log.Printf("[planner] task %s 记录重试时间失败: %v", t.ID, setErr)
		return
	}
	t.setLLMHealth(h.Failures, kind, at.Unix(), "")

	summary := fmt.Sprintf("模型调用失败，%s 后重试（第 %d 次）", backoff.Round(time.Second), h.Failures)
	if limit := e.plannerRetryAttempts(); limit > 0 {
		summary = fmt.Sprintf("模型调用失败，%s 后重试（%d/%d）", backoff.Round(time.Second), h.Failures, limit)
	}
	log.Printf("[planner] task %s %s: %v", t.ID, summary, err)
	e.emitActivity(t, db.Activity{
		Worker: "system", Kind: "llm_retry", IsError: true,
		Summary: summary, Detail: truncateLLMReason(reason),
	})
}

// stopPlannerRetries persists the blocked state, mirrors it into the live task
// and emits the single activity entry the task page shows.
func (e *Engine) stopPlannerRetries(t *Task, taskID int64, h db.TaskLLMHealth, kind, reason, summary string) {
	if blockErr := e.m.pg.BlockTaskOnLLM(taskID, reason); blockErr != nil {
		log.Printf("[planner] task %s 记录停止状态失败: %v", t.ID, blockErr)
	}
	t.setLLMHealth(h.Failures, kind, 0, reason)
	log.Printf("[planner] task %s %s: %s", t.ID, summary, truncateLLMReason(reason))
	e.emitActivity(t, db.Activity{
		Worker: "system", Kind: "llm_failover", IsError: true,
		Summary: summary, Detail: truncateLLMReason(reason),
	})
}

// clearPlannerFailure resets the counter after a round that actually planned, or
// when the user explicitly resumes the task.
func (e *Engine) clearPlannerFailure(t *Task) {
	if t == nil || e.m == nil || e.m.pg == nil {
		return
	}
	// 内存态已经干净就不用打 DB：这是每轮成功规划都会走的路径。
	if st := t.llmStateSnapshot(); st.FailureCount == 0 && st.BlockedReason == "" && st.NextAttemptAt == 0 {
		return
	}
	n, err := parseTaskID(t.ID)
	if err != nil {
		return
	}
	if clearErr := e.m.pg.ClearLLMFailure(n); clearErr != nil {
		log.Printf("[planner] task %s 清除失败计数失败: %v", t.ID, clearErr)
		return
	}
	t.setLLMHealth(0, "", 0, "")
}

// truncateLLMReason keeps a provider body out of the activity feed at full
// length — the gist travels in the summary, the exact text stays in the log.
func truncateLLMReason(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const max = 600
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
