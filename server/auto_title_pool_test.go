package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestAutoTitlesFailOverBeforeReturning(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	previousActive, err := m.pg.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	previousPool := m.LLMPoolEnabled()
	previousChatTitle := m.pg.GetBool(db.SettingChatAutoTitle, false)
	previousTaskTitle := m.pg.GetBool(db.SettingTaskAutoTitle, false)

	stamp := time.Now().UnixNano()
	priority := int(time.Now().UnixMilli() % 2_000_000_000)
	primaryID, err := m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("title-primary-%d", stamp), Format: "anthropic", Model: "primary", APIKey: "test-primary", ContextWindowK: 128})
	if err != nil {
		t.Fatal(err)
	}
	var backupID, conversationID, taskID int64
	t.Cleanup(func() {
		if taskID != 0 {
			_ = m.pg.DeleteTask(taskID)
		}
		if conversationID != 0 {
			_ = m.pg.DeleteConversation(conversationID)
		}
		_ = m.SetLLMPoolEnabled(previousPool)
		_ = m.pg.SetBool(db.SettingChatAutoTitle, previousChatTitle)
		_ = m.pg.SetBool(db.SettingTaskAutoTitle, previousTaskTitle)
		if previousActive != nil {
			_ = m.pg.SetActiveProfile(previousActive.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, primaryID)
		}
		if backupID != 0 {
			_ = m.pg.DeleteProfile(backupID)
		}
		_ = m.pg.DeleteProfile(primaryID)
	})
	backupID, err = m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("title-backup-%d", stamp), Format: "anthropic", Model: "backup", APIKey: "test-backup", ContextWindowK: 128, Priority: priority})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetActiveProfile(primaryID); err != nil {
		t.Fatal(err)
	}
	for _, set := range []func() error{
		func() error { return m.SetLLMPoolEnabled(true) },
		func() error { return m.pg.SetBool(db.SettingChatAutoTitle, true) },
		func() error { return m.pg.SetBool(db.SettingTaskAutoTitle, true) },
	} {
		if err := set(); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	td := t.TempDir()
	s := New(ctx, m, td, td, td)
	// The unrelated chat/task runners have no work to do; title generation uses
	// its own bounded context and remains testable after this cancellation.
	cancel()
	for _, id := range []int64{primaryID, backupID} {
		if _, _, ok := s.providerForProfile(id); !ok {
			t.Fatalf("profile %d unavailable", id)
		}
	}
	s.provCacheMu.Lock()
	s.provByProfile[primaryID].prov = &scriptedLLMProvider{err: errors.New("anthropic: Internal server error. Please retry your request. (500)")}
	s.provByProfile[backupID].prov = &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "备用标题"}}}
	s.provCacheMu.Unlock()

	conv, err := m.pg.CreateConversation("mainagent", "新对话", nil)
	if err != nil {
		t.Fatal(err)
	}
	conversationID = conv.ID
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"message":"请整理本周计划"}`))
	req.SetPathValue("id", strconv.FormatInt(conv.ID, 10))
	rec := httptest.NewRecorder()
	s.pgSendConversationMessage(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send status=%d body=%s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := m.pg.GetConversation(conv.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Title == "备用标题" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := m.pg.GetConversation(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "备用标题" {
		t.Fatalf("chat title=%q, want fallback title", got.Title)
	}

	description := fmt.Sprintf("整理计划-%d", stamp)
	input, _ := json.Marshal(map[string]string{"description": description, "goal": "形成周计划"})
	_, err = s.toolSpawnTask().Call(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	var taskTitle string
	if err := m.pg.QueryRow(`SELECT id,name FROM tasks WHERE description=$1 ORDER BY id DESC LIMIT 1`, description).Scan(&taskID, &taskTitle); err != nil {
		t.Fatal(err)
	}
	if taskTitle != "备用标题" {
		t.Fatalf("task title=%q, want fallback title", taskTitle)
	}
}
