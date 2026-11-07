package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStorageScanDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	n, err := directoryBytes(root, nil)
	if err != nil || n != 3 {
		t.Fatalf("directory bytes=(%d,%v), want 3", n, err)
	}
}

func TestCleanupOrphanTaskWorkspacesKeepsLiveTasksAndEvidence(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir, "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	live, err := m.CreateTask("workspace cleanup live", "workspace cleanup live", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(live.ID, DeleteTaskOptions{DeleteFiles: true})
	base := time.Now().UnixNano()
	orphan := strconv.FormatInt(base, 10)
	withCase := strconv.FormatInt(base+1, 10)
	withEvidence := strconv.FormatInt(base+2, 10)
	for _, id := range []string{live.ID, orphan, withCase, withEvidence} {
		if err := os.MkdirAll(filepath.Join(dataDir, "tasks", id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, "tasks", id, "output.txt"), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "tasks", orphan, "cmd-output"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"remove.txt", "keep.txt"} {
		if err := os.WriteFile(filepath.Join(dataDir, "tasks", orphan, "cmd-output", name), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.PG().Exec(`INSERT INTO finding_cases(origin_task_id,title,reason) VALUES($1,'test','test')`, base+1); err != nil {
		t.Fatal(err)
	}
	defer m.PG().Exec(`DELETE FROM finding_cases WHERE origin_task_id=$1`, base+1)
	if err := os.MkdirAll(filepath.Join(dataDir, "tasks", withEvidence, "findings", "screenshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "tasks", withEvidence, "findings", "screenshots", "proof.png"), []byte("proof"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, dataDir: dataDir, engine: NewEngine(m)}
	groups, err := s.scanTaskCleanup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]taskCleanupGroup{}
	for _, group := range groups {
		byPath[group.Path] = group
	}
	if byPath["tasks/"+live.ID].Risk != "locked" || byPath["tasks/"+orphan+"/output.txt"].Risk != "safe" || byPath["tasks/"+withCase+"/output.txt"].Risk != "review" || byPath["tasks/"+withEvidence+"/findings"].Risk != "locked" {
		t.Fatalf("unexpected cleanup groups: %+v", groups)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/system/storage/task-cleanup", strings.NewReader(`{"paths":["tasks/`+orphan+`/output.txt","tasks/`+orphan+`/cmd-output/remove.txt","tasks/`+withCase+`/output.txt","tasks/`+withEvidence+`/output.txt"]}`))
	w := httptest.NewRecorder()
	s.taskWorkspaceCleanup(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deleted":4`) {
		t.Fatalf("cleanup status=%d body=%s", w.Code, w.Body.String())
	}
	for _, id := range []string{live.ID, withCase, withEvidence} {
		if _, err := os.Stat(filepath.Join(dataDir, "tasks", id)); err != nil {
			t.Fatalf("protected task %s removed: %v", id, err)
		}
	}
	for _, id := range []string{orphan, withCase, withEvidence} {
		if _, err := os.Stat(filepath.Join(dataDir, "tasks", id, "output.txt")); !os.IsNotExist(err) {
			t.Fatalf("cleanable file of task %s still exists: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "tasks", orphan, "cmd-output", "remove.txt")); !os.IsNotExist(err) {
		t.Fatalf("selected file remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "tasks", orphan, "cmd-output", "keep.txt")); err != nil {
		t.Fatalf("unselected sibling removed: %v", err)
	}
}

func TestCleanupFinishedTaskScratchBeforeRetention(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir, "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("finished scratch", "finished scratch", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{DeleteFiles: true})
	if _, err := m.PG().Exec(`UPDATE tasks SET status='done', completed_at=NOW() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dataDir, "tasks", task.ID)
	scratch := filepath.Join(base, ".agent-scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(scratch, "temp.txt"), filepath.Join(scratch, "keep.txt"), filepath.Join(base, "output.txt")} {
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{m: m, dataDir: dataDir, engine: NewEngine(m)}
	groups, err := s.scanTaskCleanup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]taskCleanupGroup{}
	for _, group := range groups {
		byPath[group.Path] = group
	}
	if byPath["tasks/"+task.ID+"/.agent-scratch"].Risk != "safe" || byPath["tasks/"+task.ID+"/.agent-scratch"].Bytes != 8 || byPath["tasks/"+task.ID].Risk != "locked" || byPath["tasks/"+task.ID].ProtectedBytes != 4 {
		t.Fatalf("unexpected cleanup groups: %+v", groups)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/system/storage/task-cleanup", strings.NewReader(`{"paths":["tasks/`+task.ID+`/.agent-scratch/temp.txt"]}`))
	if _, err := m.PG().Exec(`UPDATE tasks SET status='running' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.taskWorkspaceCleanup(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("running task cleanup status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("running task scratch removed: %v", err)
	}
	if _, err := m.PG().Exec(`UPDATE tasks SET status='done' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/system/storage/task-cleanup", strings.NewReader(`{"paths":["tasks/`+task.ID+`/.agent-scratch/temp.txt"]}`))
	w = httptest.NewRecorder()
	s.taskWorkspaceCleanup(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"reclaimed_bytes":4`) {
		t.Fatalf("cleanup status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(scratch, "temp.txt")); !os.IsNotExist(err) {
		t.Fatalf("selected scratch file remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch, "keep.txt")); err != nil {
		t.Fatalf("unselected scratch file removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "output.txt")); err != nil {
		t.Fatalf("task output removed: %v", err)
	}
}

func TestRetentionRejectsInvalidDays(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPut, "/api/system/storage/retention", strings.NewReader(`{"traffic_days":-1,"llm_days":0,"tool_days":0}`))
	w := httptest.NewRecorder()
	s.recordRetentionAPI(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
}

func TestDeleteRequestRejectsAmbiguousScope(t *testing.T) {
	for _, body := range []string{
		`{"mode":"all","ids":[1]}`,
		`{"mode":"selected"}`,
		`{"mode":"filter","ids":[1]}`,
	} {
		req := httptest.NewRequest(http.MethodDelete, "/api/commands", strings.NewReader(body))
		w := httptest.NewRecorder()
		_, ok := readRecordDeleteRequest(w, req)
		if ok || w.Code != http.StatusBadRequest {
			t.Fatalf("body %s accepted: status=%d", body, w.Code)
		}
	}
}
