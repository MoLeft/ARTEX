package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type metricValue struct {
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Percent    float64 `json:"percent"`
}

type systemMetricsResponse struct {
	Scope          string      `json:"scope"`
	Supported      bool        `json:"supported"`
	Timestamp      time.Time   `json:"timestamp"`
	CPU            metricValue `json:"cpu"`
	Memory         metricValue `json:"memory"`
	Disk           metricValue `json:"disk"`
	DataDisk       metricValue `json:"data_disk"`
	DataDiskSource string      `json:"data_disk_source"`
	Container      bool        `json:"container_metrics"`
}

type hostStorageSnapshot struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	ObservedAt     int64  `json:"observed_at"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
}

func readHostStorage(dataDir string, now time.Time) (hostStorageSnapshot, bool) {
	var snapshot hostStorageSnapshot
	raw, err := os.ReadFile(filepath.Join(dataDir, ".host-storage.json"))
	if err != nil || json.Unmarshal(raw, &snapshot) != nil || snapshot.TotalBytes == 0 || snapshot.AvailableBytes > snapshot.TotalBytes {
		return hostStorageSnapshot{}, false
	}
	age := now.Sub(time.Unix(snapshot.ObservedAt, 0))
	if age < -time.Minute || age > 5*time.Minute {
		return hostStorageSnapshot{}, false
	}
	return snapshot, true
}

func hostStorageMetric(dataDir string, now time.Time) (metricValue, bool) {
	snapshot, ok := readHostStorage(dataDir, now)
	if !ok {
		return metricValue{}, false
	}
	used := snapshot.TotalBytes - snapshot.AvailableBytes
	return metricValue{UsedBytes: used, TotalBytes: snapshot.TotalBytes, Percent: float64(used) / float64(snapshot.TotalBytes) * 100}, true
}

func (s *Server) systemMetrics(w http.ResponseWriter, r *http.Request) {
	out := systemMetricsSnapshot(s)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
