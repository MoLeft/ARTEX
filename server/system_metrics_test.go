package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSystemMetricsReturnsContainerAndDiskValues(t *testing.T) {
	s := &Server{dataDir: t.TempDir()}
	r := httptest.NewRequest("GET", "/api/system/metrics", nil)
	w := httptest.NewRecorder()
	s.systemMetrics(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	var got systemMetricsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Scope == "" || got.Timestamp.IsZero() {
		t.Fatalf("unexpected response metadata: %+v", got)
	}
	if runtime.GOOS != "linux" {
		if got.Supported || got.Scope != "unsupported" {
			t.Fatalf("unsupported platform returned metrics: %+v", got)
		}
		return
	}
	if got.Disk.TotalBytes == 0 || got.DataDisk.TotalBytes == 0 {
		t.Fatalf("disk metrics missing: %+v", got)
	}
}

func TestHostStorageMetricRejectsStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(at int64) {
		t.Helper()
		payload, _ := json.Marshal(hostStorageSnapshot{TotalBytes: 256_000_000_000, AvailableBytes: 100_000_000_000, ObservedAt: at, OS: "macOS", Arch: "arm64"})
		if err := os.WriteFile(filepath.Join(dir, ".host-storage.json"), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(now.Unix())
	metric, ok := hostStorageMetric(dir, now)
	if !ok || metric.TotalBytes != 256_000_000_000 || metric.UsedBytes != 156_000_000_000 {
		t.Fatalf("host metric=%+v ok=%v", metric, ok)
	}
	write(now.Add(-6 * time.Minute).Unix())
	if _, ok := hostStorageMetric(dir, now); ok {
		t.Fatal("stale host snapshot accepted")
	}
}
