package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

type recordDeleteRequest struct {
	Mode    string  `json:"mode"`
	IDs     []int64 `json:"ids"`
	Task    string  `json:"task"`
	Query   string  `json:"q"`
	Model   string  `json:"model"`
	Session string  `json:"session"`
}

func readRecordDeleteRequest(w http.ResponseWriter, r *http.Request) (recordDeleteRequest, bool) {
	var req recordDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid body")
		return req, false
	}
	if req.Mode != "all" && req.Mode != "filter" && req.Mode != "selected" {
		writeErr(w, 400, "invalid delete mode")
		return req, false
	}
	if (req.Mode == "selected" && (len(req.IDs) == 0 || len(req.IDs) > 500)) ||
		(req.Mode != "selected" && len(req.IDs) != 0) {
		writeErr(w, 400, "invalid selected ids")
		return req, false
	}
	for _, id := range req.IDs {
		if id <= 0 {
			writeErr(w, 400, "invalid selected id")
			return req, false
		}
	}
	return req, true
}

func (s *Server) pgDeleteCommands(w http.ResponseWriter, r *http.Request) {
	req, ok := readRecordDeleteRequest(w, r)
	if !ok {
		return
	}
	var expID *int64
	if req.Mode != "all" {
		req.Task = strings.TrimSpace(req.Task)
		req.Query = strings.TrimSpace(req.Query)
		if req.Task != "" {
			id, err := strconv.ParseInt(req.Task, 10, 64)
			if err != nil || id <= 0 {
				writeErr(w, 400, "invalid task")
				return
			}
			expID = &id
		}
		if req.Mode == "filter" && expID == nil && req.Query == "" {
			writeErr(w, 400, "missing filter")
			return
		}
	} else {
		req.IDs = nil
		req.Query = ""
	}
	n, err := s.m.PG().DeleteCommands(expID, req.Query, req.IDs, req.Mode == "all")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// pgListCommands returns tool executions (any tool) from the activity table.
func (s *Server) pgListCommands(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 0)
	size := atoiDefault(q.Get("size"), 50)
	keyword := q.Get("q")

	records, total, err := s.m.PG().ListCommands(commandTaskFilter(q.Get("task")), keyword, page, size)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"commands": records, "total": total})
}

// pgToolStats returns per-tool call counts for the tool-execution history, under
// the same task/keyword filters the list takes — the summary describes the whole
// filtered set, not the page on screen.
func (s *Server) pgToolStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pg := s.m.PG()
	if pg == nil {
		writeJSON(w, 200, map[string]any{"stats": []db.ToolStat{}})
		return
	}
	stats, err := pg.ToolStats(commandTaskFilter(q.Get("task")), q.Get("q"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"stats": stats})
}

// commandTaskFilter parses the ?task= exploration id. Absent or unparsable → nil
// (no filter), matching the list endpoint's long-standing lenient behaviour.
func commandTaskFilter(v string) *int64 {
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// pgListLLMRecords returns paginated LLM call records.
func (s *Server) pgListLLMRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 0)
	size := atoiDefault(q.Get("size"), 50)
	model := q.Get("model")
	session := q.Get("session")
	task := q.Get("task")

	records, total, err := s.m.PG().ListLLMRecords(model, session, task, page, size)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"records": records, "total": total})
}

// pgTokenByModel returns a task's LLM token usage grouped by model, from the
// always-on llm_usage metering ledger. Accurate even with per-agent model bindings,
// pool rotation/failover, and interrupted runs (every call is metered on success or
// error). No recording toggle required.
func (s *Server) pgTokenByModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("task"))
	if id == "" {
		writeErr(w, 400, "missing task")
		return
	}
	pg := s.m.PG()
	if pg == nil {
		writeJSON(w, 200, map[string]any{"models": []db.ModelTokenStat{}})
		return
	}
	models, err := pg.TokenByModel(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// pgUsageStats returns global llm_usage aggregates for the dashboard's "new" token
// view: per-profile totals (all-time) + per-(profile, day) buckets for the daily
// chart. Sourced from the always-on metering ledger, so it is accurate across
// per-agent bindings, pool rotation, and interrupted runs.
func (s *Server) pgUsageStats(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeJSON(w, 200, map[string]any{"by_profile": []db.ProfileUsage{}, "daily": []db.ProfileDayUsage{}})
		return
	}
	days := atoiDefault(r.URL.Query().Get("days"), 365)
	byProfile, err := pg.UsageByProfile()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	daily, err := pg.UsageDaily(days)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"by_profile": byProfile, "daily": daily})
}

// pgLLMTasks returns distinct recorded tasks with counts, for the page's task
// picker (pick a task → filter the list, then delete its conversations).
func (s *Server) pgLLMTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.m.PG().LLMTasks()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"tasks": tasks})
}

// pgDeleteLLMRecords supports the existing task delete plus explicit list modes.
func (s *Server) pgDeleteLLMRecords(w http.ResponseWriter, r *http.Request) {
	var req recordDeleteRequest
	if task := strings.TrimSpace(r.URL.Query().Get("task")); task != "" {
		req = recordDeleteRequest{Mode: "filter", Task: task}
	} else {
		var ok bool
		req, ok = readRecordDeleteRequest(w, r)
		if !ok {
			return
		}
	}
	if req.Mode == "all" {
		req = recordDeleteRequest{Mode: "all"}
	}
	if req.Mode == "filter" && req.Model == "" && req.Session == "" && req.Task == "" {
		writeErr(w, 400, "missing filter")
		return
	}
	n, err := s.m.PG().DeleteLLMRecords(req.Model, req.Session, req.Task, req.IDs, req.Mode == "all")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// pgGetLLMRecord returns a single LLM record with full request/response bodies.
func (s *Server) pgGetLLMRecord(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid id")
		return
	}
	rec, err := s.m.PG().GetLLMRecord(id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, rec)
}
