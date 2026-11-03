package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
