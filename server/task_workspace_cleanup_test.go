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

func TestScanLiveTaskScratchSeparatesReferencedFiles(t *testing.T) {
	base := t.TempDir()
	for _, rel := range []string{".agent-scratch", "i1/.agent-scratch"} {
		if err := os.MkdirAll(filepath.Join(base, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range map[string]string{
		".agent-scratch/temporary.txt": "temp",
		".agent-scratch/proof.txt":     "proof",
		"i1/.agent-scratch/worker.txt": "worker",
	} {
		if err := os.WriteFile(filepath.Join(base, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	groups, total, err := scanLiveTaskScratch(context.Background(), base, "3", map[string]bool{".agent-scratch/proof.txt": true})
	if err != nil || len(groups) != 2 || total != 15 {
		t.Fatalf("groups=%+v total=%d err=%v", groups, total, err)
	}
	if groups[0].Path != "tasks/3/.agent-scratch" || groups[0].Risk != "review" || groups[0].Bytes != 4 || groups[0].ProtectedBytes != 5 || len(groups[0].targets) != 1 {
		t.Fatalf("root scratch=%+v", groups[0])
	}
	if groups[1].Path != "tasks/3/i1/.agent-scratch" || groups[1].Risk != "safe" || groups[1].Bytes != 6 || len(groups[1].targets) != 1 {
		t.Fatalf("worker scratch=%+v", groups[1])
	}
}

func TestResolveTaskCleanupPathsAllowsIndividualFilesOnlyWithinGroup(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "tasks", "3", "worker")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"nested/temporary.txt", "nested/proof.txt"} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	group := taskCleanupGroup{Path: "tasks/3/worker", TaskID: "3", Dir: true, Risk: "review", Bytes: 4, ProtectedPaths: []string{"tasks/3/worker/nested/proof.txt"}, targets: []string{filepath.Join(root, "nested", "temporary.txt")}}
	chosen, err := resolveTaskCleanupPaths(context.Background(), dataDir, []taskCleanupGroup{group}, []string{"tasks/3/worker/nested/temporary.txt"})
	if err != nil || len(chosen) != 1 || chosen[0].Bytes != 4 || len(chosen[0].targets) != 1 || chosen[0].targets[0] != filepath.Join(root, "nested", "temporary.txt") {
		t.Fatalf("chosen=%+v err=%v", chosen, err)
	}
	for _, paths := range [][]string{
		{"tasks/3/worker/nested/proof.txt"},
		{"tasks/3/worker", "tasks/3/worker/nested/temporary.txt"},
		{"tasks/3/worker/../findings"},
	} {
		if _, err := resolveTaskCleanupPaths(context.Background(), dataDir, []taskCleanupGroup{group}, paths); err == nil {
			t.Fatalf("accepted unsafe paths %v", paths)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTaskCleanupPaths(context.Background(), dataDir, []taskCleanupGroup{group}, []string{"tasks/3/worker/link/outside.txt"}); err == nil {
		t.Fatal("followed symlink outside task workspace")
	}
}
