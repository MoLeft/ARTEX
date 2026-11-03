package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanTaskCleanupPathKeepsReferencedFile(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "tasks", "3", "worker")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"script.py": "script", "proof.png": "proof"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if matches := taskFileReference.FindAllStringSubmatch(`![proof](workspace://tasks/3/worker/proof.png)`, -1); len(matches) != 1 || matches[0][1] != "3" || matches[0][2] != "worker/proof.png" {
		t.Fatalf("report reference not recognized: %v", matches)
	}
	clean, protected, targets, err := scanTaskCleanupPath(context.Background(), root, "worker", map[string]bool{"worker/proof.png": true})
	if err != nil || clean != 6 || protected != 5 || len(targets) != 1 || targets[0] != filepath.Join(root, "script.py") {
		t.Fatalf("scan clean=%d protected=%d targets=%v err=%v", clean, protected, targets, err)
	}
	stage, err := stageTaskPaths(dataDir, "3", targets)
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "proof.png")); err != nil {
		t.Fatalf("referenced proof removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "script.py")); !os.IsNotExist(err) {
		t.Fatalf("script retained: %v", err)
	}
}

func TestScanTaskCleanupPathDoesNotFollowSymlink(t *testing.T) {
	dataDir, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "proof.png"), []byte("proof"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dataDir, "worker")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, _, targets, err := scanTaskCleanupPath(context.Background(), link, "worker", map[string]bool{"worker/proof.png": true})
	if err != nil || len(targets) != 0 {
		t.Fatalf("symlink followed: targets=%v err=%v", targets, err)
	}
}
