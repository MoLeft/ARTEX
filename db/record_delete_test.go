package db

import (
	"strconv"
	"testing"
	"time"
)

func TestRecordDeleteScopes(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTask("record-delete-test", "verify scoped deletes", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTaskCascade(task.ID, false, false, true) //nolint:errcheck
	taskID := strconv.FormatInt(task.ID, 10)
	for _, model := range []string{"model-a", "model-b"} {
		if err := d.InsertLLMRecord(&LLMRecord{TaskID: taskID, Model: model, SessionID: "delete-test-session"}); err != nil {
			t.Fatal(err)
		}
	}
	var b int64
	if err := d.QueryRow(`SELECT id FROM llm_records WHERE task_id=$1 AND model='model-b' ORDER BY id DESC LIMIT 1`, taskID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if n, err := d.DeleteLLMRecords("model-a", "", taskID, []int64{b}, false); err != nil || n != 0 {
		t.Fatalf("selected outside filter=(%d,%v)", n, err)
	}
	if n, err := d.DeleteLLMRecords("model-a", "", taskID, nil, false); err != nil || n != 1 {
		t.Fatalf("filtered LLM delete=(%d,%v)", n, err)
	}
	var left int
	if err := d.QueryRow(`SELECT COUNT(*) FROM llm_records WHERE id=$1`, b).Scan(&left); err != nil || left != 1 {
		t.Fatalf("other model removed: %d %v", left, err)
	}
	if n, err := d.DeleteLLMRecords("", "", taskID, []int64{b}, false); err != nil || n != 1 {
		t.Fatalf("selected LLM delete=(%d,%v)", n, err)
	}
	for _, model := range []string{"expired", "recent"} {
		if err := d.InsertLLMRecord(&LLMRecord{TaskID: taskID, Model: model}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`UPDATE llm_records SET ts=now()-interval '2 days' WHERE task_id=$1 AND model='expired'`, taskID); err != nil {
		t.Fatal(err)
	}
	if n, err := d.DeleteLLMRecordsBefore(time.Now().Add(-24 * time.Hour)); err != nil || n != 1 {
		t.Fatalf("expired LLM delete=(%d,%v)", n, err)
	}
	if n, err := d.DeleteLLMRecords("", "", "", nil, true); err != nil || n != 1 {
		t.Fatalf("all LLM delete=(%d,%v)", n, err)
	}
	for _, pair := range []struct{ tool, call string }{{"Bash", "call-1"}, {"Web", "call-2"}} {
		for _, kind := range []string{"tool_use", "tool_result"} {
			if _, err := d.Exec(`INSERT INTO activity(exploration_id,kind,tool,tool_use_id,detail) VALUES($1,$2,$3,$4,'{}')`, task.ExplorationID, kind, pair.tool, pair.call); err != nil {
				t.Fatal(err)
			}
		}
	}
	var useID int64
	if err := d.QueryRow(`SELECT id FROM activity WHERE exploration_id=$1 AND kind='tool_use' AND tool='Bash'`, task.ExplorationID).Scan(&useID); err != nil {
		t.Fatal(err)
	}
	if n, err := d.DeleteCommands(&task.ExplorationID, "Bash", []int64{useID}, false); err != nil || n != 1 {
		t.Fatalf("selected command delete=(%d,%v)", n, err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM activity WHERE exploration_id=$1 AND tool_use_id='call-1'`, task.ExplorationID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("paired result survived: %d %v", left, err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM activity WHERE exploration_id=$1 AND tool_use_id='call-2'`, task.ExplorationID).Scan(&left); err != nil || left != 2 {
		t.Fatalf("other call removed: %d %v", left, err)
	}
	if _, err := d.Exec(`UPDATE tasks SET status='done' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE activity SET created_at=now()-interval '2 days' WHERE exploration_id=$1 AND tool_use_id='call-2'`, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if n, err := d.DeleteCommandsBefore(time.Now().Add(-24 * time.Hour)); err != nil || n != 1 {
		t.Fatalf("expired command delete=(%d,%v)", n, err)
	}
	if _, err := d.Exec(`INSERT INTO activity(exploration_id,kind,tool,tool_use_id,detail) VALUES($1,'tool_use','Web','call-3','{}'),($1,'tool_result','Web','call-3','{}')`, task.ExplorationID); err != nil {
		t.Fatal(err)
	}
	if n, err := d.DeleteCommands(nil, "", nil, true); err != nil || n != 1 {
		t.Fatalf("all command delete=(%d,%v)", n, err)
	}
}
