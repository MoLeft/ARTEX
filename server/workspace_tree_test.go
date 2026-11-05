package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskWorkspaceEntriesBoundedAndSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "i1"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"i1/proof.txt", "i1/second.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("proof"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "i1", "proof.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	entries, truncated, err := taskWorkspaceEntries(root, "tasks/7", 10)
	if err != nil || truncated || len(entries) != 3 {
		t.Fatalf("full tree: entries=%v truncated=%v err=%v", entries, truncated, err)
	}
	if entries[1].Path != "tasks/7/i1/proof.txt" {
		t.Fatalf("unexpected path %q", entries[1].Path)
	}
	entries, truncated, err = taskWorkspaceEntries(root, "tasks/7", 2)
	if err != nil || !truncated || len(entries) != 2 {
		t.Fatalf("bounded tree: entries=%v truncated=%v err=%v", entries, truncated, err)
	}
}

func TestWorkspaceListSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "proof.txt"), []byte("proof"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "proof.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{dir: root}}
	w := httptest.NewRecorder()
	s.wsList(w, httptest.NewRequest(http.MethodGet, "/api/workspace/list", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "proof.txt") || strings.Contains(w.Body.String(), "link.txt") {
		t.Fatalf("workspace list: status=%d body=%s", w.Code, w.Body.String())
	}
}
