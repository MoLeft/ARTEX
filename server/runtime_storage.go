package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/Autumn-27/artex/traffic"
)

const recordRetentionKey = "record_retention_v1"

type recordRetention struct {
	TrafficDays int `json:"traffic_days"`
	LLMDays     int `json:"llm_days"`
	ToolDays    int `json:"tool_days"`
}

func (s *Server) loadRecordRetention() (recordRetention, error) {
	var cfg recordRetention
	raw, ok, err := s.m.PG().GetSetting(recordRetentionKey)
	if err != nil || !ok {
		return cfg, err
	}
	err = json.Unmarshal([]byte(raw), &cfg)
	return cfg, err
}

func (s *Server) recordRetentionAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfg, err := s.loadRecordRetention()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, cfg)
		return
	}
	var cfg recordRetention
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeErr(w, 400, "invalid body")
		return
	}
	for _, days := range []int{cfg.TrafficDays, cfg.LLMDays, cfg.ToolDays} {
		if days < 0 || days > 3650 {
			writeErr(w, 400, "retention days must be 0-3650")
			return
		}
	}
	raw, _ := json.Marshal(cfg)
	if err := s.m.PG().SetSettingContext(r.Context(), recordRetentionKey, string(raw)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	select {
	case s.retentionWake <- struct{}{}:
	default:
	}
	writeJSON(w, 200, cfg)
}

func (s *Server) runRecordRetention() {
	cfg, err := s.loadRecordRetention()
	if err != nil {
		log.Printf("[retention] read settings: %v", err)
		return
	}
	now := time.Now()
	if cfg.TrafficDays > 0 && s.m.Traffic() != nil {
		cutoff := now.AddDate(0, 0, -cfg.TrafficDays).Unix()
		if n, err := s.m.Traffic().DeleteMatching(traffic.PageQuery{Before: cutoff, RespMin: -1, RespMax: -1}, nil); err != nil {
			log.Printf("[retention] traffic: %v", err)
		} else if n > 0 {
			log.Printf("[retention] removed %d traffic records", n)
		}
	}
	if cfg.LLMDays > 0 {
		if n, err := s.m.PG().DeleteLLMRecordsBefore(now.AddDate(0, 0, -cfg.LLMDays)); err != nil {
			log.Printf("[retention] LLM: %v", err)
		} else if n > 0 {
			log.Printf("[retention] removed %d LLM records", n)
		}
	}
	if cfg.ToolDays > 0 {
		if n, err := s.m.PG().DeleteCommandsBefore(now.AddDate(0, 0, -cfg.ToolDays)); err != nil {
			log.Printf("[retention] tools: %v", err)
		} else if n > 0 {
			log.Printf("[retention] removed %d tool calls", n)
		}
	}
}

func (s *Server) runRecordRetentionLoop() {
	initial := time.NewTimer(time.Minute)
	defer initial.Stop()
	tick := time.NewTicker(24 * time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-initial.C:
			s.runRecordRetention()
		case <-tick.C:
			s.runRecordRetention()
		case <-s.retentionWake:
			s.runRecordRetention()
		}
	}
}

type storagePart struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Bytes     uint64 `json:"bytes"`
	Cleanable bool   `json:"cleanable"`
}

func directoryBytes(root string, done <-chan struct{}) (uint64, error) {
	var size uint64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-done:
			return fmt.Errorf("scan canceled")
		default:
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			size += uint64(info.Size())
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return size, err
}

func (s *Server) storageStatus(w http.ResponseWriter, r *http.Request) {
	parts := []storagePart{
		{Key: "traffic", Name: "流量录制", Cleanable: true},
		{Key: "tasks", Name: "任务工作区"},
		{Key: "archives", Name: "任务归档"},
		{Key: "transcripts", Name: "对话转录"},
		{Key: "other", Name: "其他 ARTEX 文件"},
	}
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, entry := range entries {
		bytes, err := directoryBytes(filepath.Join(s.dataDir, entry.Name()), r.Context().Done())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		key := entry.Name()
		if key != "traffic" && key != "tasks" && key != "archives" && key != "transcripts" {
			key = "other"
		}
		for i := range parts {
			if parts[i].Key == key {
				parts[i].Bytes += bytes
				break
			}
		}
	}
	var dbBytes, llmBytes, activityBytes uint64
	if err := s.m.PG().QueryRow(`SELECT pg_database_size(current_database()), pg_total_relation_size('llm_records'::regclass), pg_total_relation_size('activity'::regclass)`).Scan(&dbBytes, &llmBytes, &activityBytes); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	otherDB := uint64(0)
	if dbBytes > llmBytes+activityBytes {
		otherDB = dbBytes - llmBytes - activityBytes
	}
	parts = append(parts, storagePart{Key: "llm", Name: "LLM 录制", Bytes: llmBytes, Cleanable: true},
		storagePart{Key: "tool", Name: "任务活动（含工具执行）", Bytes: activityBytes, Cleanable: true},
		storagePart{Key: "database_other", Name: "数据库其他数据", Bytes: otherDB})
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	writeJSON(w, 200, map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "process_heap_bytes": mem.HeapAlloc, "parts": parts})
}

func (s *Server) storageCleanup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Category string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid body")
		return
	}
	var deleted int64
	switch req.Category {
	case "memory":
		debug.FreeOSMemory()
	case "traffic":
		if s.m.Traffic() == nil {
			writeErr(w, 404, "traffic disabled")
			return
		}
		var err error
		deleted, _, err = s.m.Traffic().DeleteAll()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	case "llm":
		var err error
		deleted, err = s.m.PG().DeleteLLMRecords("", "", "", nil, true)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	case "tool":
		var err error
		deleted, err = s.m.PG().DeleteCommands(nil, "", nil, true)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	default:
		writeErr(w, 400, "category cannot be cleaned")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}
