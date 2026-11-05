package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

const workspaceCleanupKey = "workspace_auto_cleanup_v1"

var workerDirName = regexp.MustCompile(`^i[1-9][0-9]*$`)

type workspaceCleanupConfig struct {
	Enabled       bool `json:"enabled"`
	IntervalHours int  `json:"interval_hours"`
	RetentionDays int  `json:"retention_days"`
}

func (c workspaceCleanupConfig) validate() error {
	if c.IntervalHours < 1 || c.IntervalHours > 168 {
		return fmt.Errorf("cleanup interval must be 1-168 hours")
	}
	if c.RetentionDays < 1 || c.RetentionDays > 3650 {
		return fmt.Errorf("workspace retention must be 1-3650 days")
	}
	return nil
}

func (s *Server) loadWorkspaceCleanupConfig() (workspaceCleanupConfig, error) {
	cfg := workspaceCleanupConfig{IntervalHours: 24, RetentionDays: 7}
	raw, ok, err := s.m.pg.GetSetting(workspaceCleanupKey)
	if err != nil || !ok {
		return cfg, err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return workspaceCleanupConfig{IntervalHours: 24, RetentionDays: 7}, err
	}
	return cfg, cfg.validate()
}

func (s *Server) saveWorkspaceCleanupConfig(ctx context.Context, cfg workspaceCleanupConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := s.m.pg.SetSettingContext(ctx, workspaceCleanupKey, string(raw)); err != nil {
		return err
	}
	select {
	case s.workspaceCleanupWake <- struct{}{}:
	default:
	}
	return nil
}

func (s *Server) workspaceCleanupEnabled() bool {
	cfg, err := s.loadWorkspaceCleanupConfig()
	return err == nil && cfg.Enabled
}

func (s *Server) runWorkspaceCleanupLoop() {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		case <-s.workspaceCleanupWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		cfg, err := s.loadWorkspaceCleanupConfig()
		if err != nil {
			log.Printf("[workspace-cleanup] read settings: %v", err)
		} else if cfg.Enabled {
			if err := s.runWorkspaceCleanup(s.ctx, cfg); err != nil && s.ctx.Err() == nil {
				log.Printf("[workspace-cleanup] %v", err)
			}
		}
		if err != nil {
			cfg.IntervalHours = 24
		}
		timer.Reset(time.Duration(cfg.IntervalHours) * time.Hour)
	}
}

func scratchOldEnough(ctx context.Context, path string, cutoff time.Time) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || !info.IsDir() {
		return false, err
	}
	old := true
	err = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			old = false
			return filepath.SkipAll
		}
		return nil
	})
	return old, err
}

func (s *Server) workspaceTaskEligible(id int64, cutoff time.Time) (bool, error) {
	var status string
	var completed sql.NullTime
	err := s.m.pg.QueryRow(`SELECT status, completed_at FROM tasks WHERE id=$1`, id).Scan(&status, &completed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status != "done" && status != "failed" && status != "timeout" || !completed.Valid || completed.Time.After(cutoff) {
		return false, nil
	}
	key := strconv.FormatInt(id, 10)
	s.chatMu.Lock()
	chatBusy := s.chatBusy[key]
	s.chatMu.Unlock()
	return s.engine.inflightCount(key) == 0 && !chatBusy, nil
}

func (s *Server) runWorkspaceCleanup(ctx context.Context, cfg workspaceCleanupConfig) error {
	cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
	tasksRoot := filepath.Join(s.dataDir, "tasks")
	rootInfo, err := os.Lstat(tasksRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("task workspace root is not a directory")
	}
	rows, err := s.m.pg.Query(`SELECT id FROM tasks WHERE status IN ('done','failed','timeout') AND completed_at <= $1`, cutoff)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 {
		return err
	}
	_, _, refs, err := s.taskCleanupReferences()
	if err != nil {
		return err
	}
	type candidate struct {
		id   int64
		path string
		rel  string
	}
	var candidates []candidate
	var reclaimed uint64
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		eligible, err := s.workspaceTaskEligible(id, cutoff)
		if err != nil {
			return err
		}
		if !eligible {
			continue
		}
		base := filepath.Join(tasksRoot, strconv.FormatInt(id, 10))
		info, err := os.Lstat(base)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			continue
		}
		children, err := os.ReadDir(base)
		if err != nil {
			return err
		}
		for _, rel := range append([]string{""}, workerDirs(children)...) {
			parent := filepath.Join(base, rel)
			name := filepath.ToSlash(filepath.Join(rel, ".agent-scratch"))
			if protectedPath(name, refs[strconv.FormatInt(id, 10)]) || hasProtectedDescendant(name, refs[strconv.FormatInt(id, 10)]) {
				continue
			}
			path := filepath.Join(parent, ".agent-scratch")
			old, err := scratchOldEnough(ctx, path, cutoff)
			if err != nil {
				return err
			}
			if !old {
				continue
			}
			n, err := directoryBytes(path, ctx.Done())
			if err != nil {
				return err
			}
			reclaimed += n
			candidates = append(candidates, candidate{id, path, name})
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	// Archive/delete uses the same barrier; keep task directories in place until the staged deletion commits.
	s.engine.deleteMu.RLock()
	defer s.engine.deleteMu.RUnlock()
	for _, c := range candidates {
		if s.engine.IsDeleting(strconv.FormatInt(c.id, 10)) {
			return nil
		}
	}
	targets := make([]string, 0, len(candidates))
	for _, c := range candidates {
		targets = append(targets, c.path)
	}
	stage, err := stageTaskPaths(s.dataDir, "auto-workspace", targets)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		if err := stage.rollback(); err != nil {
			return fmt.Errorf("%w; rollback: %v", cause, err)
		}
		return cause
	}
	current, err := s.loadWorkspaceCleanupConfig()
	if err != nil || current != cfg {
		return rollback(fmt.Errorf("workspace cleanup settings changed"))
	}
	_, _, refs, err = s.taskCleanupReferences()
	if err != nil {
		return rollback(err)
	}
	for i, c := range candidates {
		eligible, err := s.workspaceTaskEligible(c.id, cutoff)
		if err != nil {
			return rollback(err)
		}
		locked := refs[strconv.FormatInt(c.id, 10)]
		if !eligible || protectedPath(c.rel, locked) || hasProtectedDescendant(c.rel, locked) {
			return rollback(fmt.Errorf("task %d changed during workspace cleanup", c.id))
		}
		old, err := scratchOldEnough(ctx, stage.moves[i].staged, cutoff)
		if err != nil || !old {
			return rollback(fmt.Errorf("task %d temporary files changed during cleanup: %v", c.id, err))
		}
	}
	if err := stage.commit(); err != nil {
		return err
	}
	log.Printf("[workspace-cleanup] removed %d temporary directories (%d bytes)", len(candidates), reclaimed)
	return nil
}

func workerDirs(entries []os.DirEntry) []string {
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && workerDirName.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names
}
