package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type taskCleanupGroup struct {
	Path           string   `json:"path"`
	TaskID         string   `json:"task_id"`
	Dir            bool     `json:"dir"`
	Bytes          uint64   `json:"bytes"`
	ProtectedBytes uint64   `json:"protected_bytes"`
	Risk           string   `json:"risk"`
	Reason         string   `json:"reason"`
	ProtectedPaths []string `json:"protected_paths,omitempty"`
	targets        []string
	liveScratch    bool
}

var taskFileReference = regexp.MustCompile(`tasks/([1-9][0-9]*)/([^\s\)\]\}"'<>,]+)`)

func (s *Server) taskCleanupReferences() (map[string]bool, map[string]bool, map[string]map[string]bool, error) {
	live := map[string]bool{}
	rows, err := s.m.PG().Query(`SELECT id FROM tasks`)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		live[strconv.FormatInt(id, 10)] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}

	cases := map[string]bool{}
	protected := map[string]map[string]bool{}
	rows, err = s.m.PG().Query(`SELECT origin_task_id FROM finding_cases`)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		key := strconv.FormatInt(id, 10)
		cases[key] = true
		if protected[key] == nil {
			protected[key] = map[string]bool{}
		}
		protected[key]["findings"] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}

	rows, err = s.m.PG().Query(`SELECT report FROM findings WHERE report<>'' UNION ALL SELECT evidence FROM findings WHERE evidence<>'' UNION ALL SELECT report FROM finding_cases WHERE report<>'' UNION ALL SELECT detail FROM conversation_activities WHERE detail LIKE '%tasks/%' UNION ALL SELECT summary FROM conversation_activities WHERE summary LIKE '%tasks/%'`)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		for _, match := range taskFileReference.FindAllStringSubmatch(body, -1) {
			id, ok := canonicalTaskID(match[1])
			if !ok || id != match[1] {
				continue
			}
			rel := filepath.Clean(strings.TrimRight(match[2], ".,;:"))
			if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				continue
			}
			if protected[id] == nil {
				protected[id] = map[string]bool{}
			}
			protected[id][filepath.ToSlash(rel)] = true
		}
	}
	err = rows.Err()
	rows.Close()
	return live, cases, protected, err
}

func protectedPath(rel string, locked map[string]bool) bool {
	for path := range locked {
		if rel == path || strings.HasPrefix(rel, path+"/") {
			return true
		}
	}
	return false
}

func hasProtectedDescendant(rel string, locked map[string]bool) bool {
	for path := range locked {
		if strings.HasPrefix(path, rel+"/") {
			return true
		}
	}
	return false
}

func scanTaskCleanupPath(ctx context.Context, abs, rel string, locked map[string]bool) (uint64, uint64, []string, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, nil, err
	}
	if protectedPath(rel, locked) {
		n, err := directoryBytes(abs, ctx.Done())
		return 0, n, nil, err
	}
	if !hasProtectedDescendant(rel, locked) {
		n, err := directoryBytes(abs, ctx.Done())
		return n, 0, []string{abs}, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return 0, 0, nil, err
	}
	if !info.IsDir() {
		return 0, uint64(info.Size()), nil, nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return 0, 0, nil, err
	}
	var clean, protected uint64
	var targets []string
	for _, entry := range entries {
		childRel := rel + "/" + entry.Name()
		a, b, paths, err := scanTaskCleanupPath(ctx, filepath.Join(abs, entry.Name()), childRel, locked)
		if err != nil {
			return 0, 0, nil, err
		}
		clean += a
		protected += b
		targets = append(targets, paths...)
	}
	return clean, protected, targets, nil
}

func scanLiveTaskScratch(ctx context.Context, base, id string, refs map[string]bool) ([]taskCleanupGroup, uint64, error) {
	children, err := os.ReadDir(base)
	if err != nil {
		return nil, 0, err
	}
	var groups []taskCleanupGroup
	var total uint64
	for _, parent := range append([]string{""}, workerDirs(children)...) {
		rel := filepath.ToSlash(filepath.Join(parent, ".agent-scratch"))
		abs := filepath.Join(base, rel)
		info, err := os.Lstat(abs)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		if !info.IsDir() {
			continue
		}
		clean, protected, targets, err := scanTaskCleanupPath(ctx, abs, rel, refs)
		if err != nil {
			return nil, 0, err
		}
		if clean+protected == 0 {
			continue
		}
		total += clean + protected
		risk, reason := "safe", "已结束任务的临时文件，无已知引用"
		if protected > 0 {
			risk, reason = "review", "临时目录中有直接引用；只清理未引用文件"
		}
		if len(targets) == 0 {
			risk, reason = "locked", "临时目录全部被引用"
		}
		var paths []string
		for path := range refs {
			if path == rel || strings.HasPrefix(path, rel+"/") {
				paths = append(paths, "tasks/"+id+"/"+path)
			}
		}
		sort.Strings(paths)
		groups = append(groups, taskCleanupGroup{Path: "tasks/" + id + "/" + rel, TaskID: id, Dir: true, Bytes: clean, ProtectedBytes: protected, Risk: risk, Reason: reason, ProtectedPaths: paths, targets: targets, liveScratch: true})
	}
	return groups, total, nil
}

func (s *Server) scanTaskCleanup(ctx context.Context) ([]taskCleanupGroup, error) {
	live, cases, refs, err := s.taskCleanupReferences()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(s.dataDir, "tasks")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []taskCleanupGroup{}, nil
	}
	if err != nil {
		return nil, err
	}
	groups := []taskCleanupGroup{}
	for _, task := range entries {
		id, ok := canonicalTaskID(task.Name())
		if !task.IsDir() || !ok || id != task.Name() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		base := filepath.Join(root, id)
		if live[id] {
			taskID, _ := strconv.ParseInt(id, 10, 64)
			eligible, err := s.workspaceTaskEligible(taskID, time.Now())
			if err != nil {
				return nil, err
			}
			var scratchBytes uint64
			if eligible {
				scratch, n, err := scanLiveTaskScratch(ctx, base, id, refs[id])
				if err != nil {
					return nil, err
				}
				groups = append(groups, scratch...)
				scratchBytes = n
			}
			n, err := directoryBytes(base, ctx.Done())
			if err != nil {
				return nil, err
			}
			if n < scratchBytes {
				return nil, fmt.Errorf("task %s workspace changed during scan", id)
			}
			groups = append(groups, taskCleanupGroup{Path: "tasks/" + id, TaskID: id, Dir: true, ProtectedBytes: n - scratchBytes, Risk: "locked", Reason: "任务仍存在", ProtectedPaths: []string{"tasks/" + id}})
			continue
		}
		children, err := os.ReadDir(base)
		if err != nil {
			return nil, err
		}
		locked := refs[id]
		hasRefs := len(locked) > 0
		if locked == nil {
			locked = map[string]bool{}
		}
		locked["findings"] = true // Detached findings lose task_id; keep all report evidence.
		var paths []string
		for path := range locked {
			paths = append(paths, "tasks/"+id+"/"+path)
		}
		sort.Strings(paths)
		for _, child := range children {
			rel := child.Name()
			abs := filepath.Join(base, rel)
			clean, protected, targets, err := scanTaskCleanupPath(ctx, abs, rel, locked)
			if err != nil {
				return nil, err
			}
			risk, reason := "safe", "已删除任务，无已知引用"
			if cases[id] || hasRefs || protected > 0 {
				risk, reason = "review", "任务保留了漏洞案例或直接引用；只清理未引用文件"
			}
			if len(targets) == 0 {
				risk, reason = "locked", "漏洞证据或直接引用"
			}
			groups = append(groups, taskCleanupGroup{Path: "tasks/" + id + "/" + rel, TaskID: id, Dir: child.IsDir(), Bytes: clean, ProtectedBytes: protected, Risk: risk, Reason: reason, ProtectedPaths: paths, targets: targets})
		}
	}
	return groups, nil
}

func (s *Server) taskWorkspaceCleanupPreview(w http.ResponseWriter, r *http.Request) {
	groups, err := s.scanTaskCleanup(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"groups": groups})
}

func resolveTaskCleanupPaths(ctx context.Context, dataDir string, groups []taskCleanupGroup, paths []string) ([]taskCleanupGroup, error) {
	selected := make(map[string]bool, len(paths))
	for _, path := range paths {
		if filepath.ToSlash(filepath.Clean(path)) != path || filepath.IsAbs(path) || selected[path] {
			return nil, fmt.Errorf("invalid or duplicate cleanup path: %s", path)
		}
		selected[path] = true
	}
	for path := range selected {
		for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
			if selected[parent] {
				return nil, fmt.Errorf("overlapping cleanup paths: %s", path)
			}
		}
	}
	chosen := make([]taskCleanupGroup, 0, len(paths))
	for _, path := range paths {
		abs := dataDir
		for _, part := range strings.Split(path, "/") {
			abs = filepath.Join(abs, part)
			info, err := os.Lstat(abs)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("selected path changed or is a symlink: %s", path)
			}
		}
		var group *taskCleanupGroup
		for i := range groups {
			candidate := &groups[i]
			if (path == candidate.Path || strings.HasPrefix(path, candidate.Path+"/")) && (group == nil || len(candidate.Path) > len(group.Path)) {
				group = candidate
			}
		}
		if group == nil || group.Risk == "locked" || len(group.targets) == 0 {
			return nil, fmt.Errorf("selected path is no longer cleanable: %s", path)
		}
		item := *group
		if path != group.Path {
			if !group.Dir {
				return nil, fmt.Errorf("selected path is not inside a directory: %s", path)
			}
			locked := map[string]bool{}
			prefix := "tasks/" + group.TaskID + "/"
			for _, protected := range group.ProtectedPaths {
				locked[strings.TrimPrefix(protected, prefix)] = true
			}
			clean, protected, targets, err := scanTaskCleanupPath(ctx, abs, strings.TrimPrefix(path, prefix), locked)
			if err != nil || len(targets) == 0 {
				return nil, fmt.Errorf("selected path changed or is protected: %s: %v", path, err)
			}
			item.Path, item.Bytes, item.ProtectedBytes, item.targets = path, clean, protected, targets
		}
		chosen = append(chosen, item)
	}
	return chosen, nil
}

func (s *Server) taskWorkspaceCleanup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > 2000 {
		writeErr(w, 400, "select 1-2000 cleanup groups")
		return
	}
	scratchSelected := false
	for _, path := range req.Paths {
		scratchSelected = scratchSelected || strings.HasSuffix(path, "/.agent-scratch") || strings.Contains(path, "/.agent-scratch/")
	}
	if scratchSelected {
		s.engine.deleteMu.Lock()
		defer s.engine.deleteMu.Unlock()
	}
	groups, err := s.scanTaskCleanup(r.Context()) // Recheck references at the destructive boundary.
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	chosen, err := resolveTaskCleanupPaths(r.Context(), s.dataDir, groups, req.Paths)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	var targets []string
	for _, group := range chosen {
		targets = append(targets, group.targets...)
	}
	stage, err := stageTaskPaths(s.dataDir, "workspace", targets)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	rollback := func(cause error) {
		if err := stage.rollback(); err != nil {
			cause = fmt.Errorf("%w; rollback: %v", cause, err)
		}
		writeErr(w, 500, cause.Error())
	}
	live, cases, refs, err := s.taskCleanupReferences()
	if err != nil {
		rollback(err)
		return
	}
	for _, group := range chosen {
		if group.liveScratch {
			id, _ := strconv.ParseInt(group.TaskID, 10, 64)
			eligible, err := s.workspaceTaskEligible(id, time.Now())
			if err != nil || !live[group.TaskID] || !eligible {
				rollback(fmt.Errorf("task %s is no longer idle and completed: %v", group.TaskID, err))
				return
			}
		} else if live[group.TaskID] || (group.Risk == "safe" && cases[group.TaskID]) {
			rollback(fmt.Errorf("task %s references changed; rescan required", group.TaskID))
			return
		}
		for _, target := range group.targets {
			rel, err := filepath.Rel(filepath.Join(s.dataDir, "tasks", group.TaskID), target)
			if err != nil || protectedPath(filepath.ToSlash(rel), refs[group.TaskID]) || hasProtectedDescendant(filepath.ToSlash(rel), refs[group.TaskID]) {
				rollback(fmt.Errorf("task %s file references changed; rescan required", group.TaskID))
				return
			}
		}
	}
	var deleted int64
	var reclaimed uint64
	if err := stage.commit(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, group := range chosen {
		deleted++
		reclaimed += group.Bytes
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted, "reclaimed_bytes": reclaimed})
}
