package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceCleanupConfigValidation(t *testing.T) {
	for _, cfg := range []workspaceCleanupConfig{
		{IntervalHours: 0, RetentionDays: 7},
		{IntervalHours: 169, RetentionDays: 7},
		{IntervalHours: 24, RetentionDays: 0},
		{IntervalHours: 24, RetentionDays: 3651},
	} {
		if cfg.validate() == nil {
			t.Fatalf("accepted invalid config: %+v", cfg)
		}
	}
	if err := (workspaceCleanupConfig{Enabled: true, IntervalHours: 1, RetentionDays: 1}).validate(); err != nil {
		t.Fatal(err)
	}
}

func TestScratchOldEnoughPreservesRecentFilesAndSymlinks(t *testing.T) {
	base := t.TempDir()
	scratch := filepath.Join(base, ".agent-scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(scratch, "download.bin")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{file, scratch} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	if ok, err := scratchOldEnough(context.Background(), scratch, cutoff); err != nil || !ok {
		t.Fatalf("old scratch eligible=%v err=%v", ok, err)
	}
	stage, err := stageTaskPaths(base, "scratch-test", []string{scratch})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := scratchOldEnough(context.Background(), stage.moves[0].staged, cutoff); err != nil || !ok {
		t.Fatalf("staged scratch eligible=%v err=%v", ok, err)
	}
	if err := stage.rollback(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, err := scratchOldEnough(context.Background(), scratch, cutoff); err != nil || ok {
		t.Fatalf("recent download eligible=%v err=%v", ok, err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(scratch, link); err != nil {
		t.Fatal(err)
	}
	if ok, err := scratchOldEnough(context.Background(), link, cutoff); err != nil || ok {
		t.Fatalf("symlink eligible=%v err=%v", ok, err)
	}
}

func TestWorkerDirsOnlySelectsIntentDirectories(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"i1", "i02", "findings", "i3.txt"} {
		if name == "i3.txt" {
			if err := os.WriteFile(filepath.Join(base, name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Mkdir(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	names := workerDirs(entries)
	if len(names) != 1 || names[0] != "i1" {
		t.Fatalf("worker directories: %v", names)
	}
}
