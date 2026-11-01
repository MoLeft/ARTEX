package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/Autumn-27/artex/mcphttp"
)

const lightpandaURL = "http://127.0.0.1:9222/mcp"

// The beta image bundles Lightpanda. The process belongs to this ARTEX binary,
// so switching back to an older binary removes it with the beta runtime.
func startLightpanda(ctx context.Context) {
	if os.Getenv("ARTEX_LIGHTPANDA_MCP_URL") != "" {
		return // explicitly configured external MCP
	}
	if _, err := exec.LookPath("lightpanda"); err != nil {
		return // standalone builds can still opt in with an external URL
	}
	cmd := exec.CommandContext(ctx, "lightpanda", "mcp", "--host", "127.0.0.1", "--port", "9222", "--block-private-networks", "--block-cidrs", "-198.18.0.0/15")
	cmd.Env = append(os.Environ(), "LIGHTPANDA_DISABLE_TELEMETRY=true")
	if err := cmd.Start(); err != nil {
		log.Printf("[lightpanda] 启动失败: %v", err)
		return
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		done <- err
		if ctx.Err() == nil {
			_ = os.Unsetenv("ARTEX_LIGHTPANDA_MCP_URL")
			log.Printf("[lightpanda] 进程退出: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return
		default:
		}
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		client, err := mcphttp.New(probeCtx, "lightpanda_probe", lightpandaURL, nil, false)
		cancel()
		if err == nil {
			_ = client.Close()
			_ = os.Setenv("ARTEX_LIGHTPANDA_MCP_URL", lightpandaURL)
			select {
			case <-done:
				_ = os.Unsetenv("ARTEX_LIGHTPANDA_MCP_URL")
				return
			default:
			}
			log.Printf("[lightpanda] 内置 MCP 已就绪")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Printf("[lightpanda] MCP 启动超时，略过内置工具")
	_ = cmd.Process.Kill()
}
