package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLightpandaProxyArgs(t *testing.T) {
	for _, tc := range []struct {
		proxy, cert string
		want        string
	}{
		{"", "", ""},
		{"http://127.0.0.1:10001", "", "--http-proxy http://127.0.0.1:10001"},
		{"http://127.0.0.1:10002", "/tmp/capture-ca.pem", "--http-proxy http://127.0.0.1:10002 --ca-cert /tmp/capture-ca.pem"},
	} {
		args := strings.Join(lightpandaArgs(tc.proxy, tc.cert), " ")
		if tc.want != "" && !strings.Contains(args, tc.want) {
			t.Fatalf("args %q missing %q", args, tc.want)
		}
		if tc.want == "" && (strings.Contains(args, "--http-proxy") || strings.Contains(args, "--ca-cert")) {
			t.Fatalf("direct args include proxy: %q", args)
		}
	}
}

func TestLightpandaProxyUpdate(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "launches")
	path := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$LIGHTPANDA_TEST_LOG\"\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("LIGHTPANDA_TEST_LOG", logPath)
	t.Setenv("ARTEX_LIGHTPANDA_MCP_URL", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &lightpandaSupervisor{ctx: ctx, probe: func(context.Context) bool { return true }}
	waitLines := func(want int) []string {
		t.Helper()
		var lines []string
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			b, _ := os.ReadFile(logPath)
			if len(b) == 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			lines = strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) == want {
				return lines
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("launches = %q, want %d", lines, want)
		return nil
	}
	s.Update("http://127.0.0.1:10001", "")
	waitLines(1)
	s.Update("http://127.0.0.1:10001", "")
	s.Update("http://127.0.0.1:10002", "/tmp/capture-ca.pem")
	lines := waitLines(2)
	if len(lines) != 2 || !strings.Contains(lines[0], "--http-proxy http://127.0.0.1:10001") || !strings.Contains(lines[1], "--http-proxy http://127.0.0.1:10002 --ca-cert /tmp/capture-ca.pem") {
		t.Fatalf("launches = %q", lines)
	}
}
