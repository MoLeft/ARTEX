package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/Autumn-27/artex/mcphttp"
)

const lightpandaURL = "http://127.0.0.1:9222/mcp"

type lightpandaSupervisor struct {
	ctx   context.Context
	mu    sync.Mutex
	cmd   *exec.Cmd
	done  chan struct{}
	proxy string
	cert  string
	probe func(context.Context) bool
}

func lightpandaArgs(proxyAddr, caCert string) []string {
	args := []string{"mcp", "--host", "127.0.0.1", "--port", "9222", "--block-private-networks", "--block-cidrs", "-198.18.0.0/15"}
	if proxyAddr != "" {
		args = append(args, "--http-proxy", proxyAddr)
	}
	if caCert != "" {
		args = append(args, "--ca-cert", caCert)
	}
	return args
}

// The beta image bundles Lightpanda. The process belongs to this ARTEX binary,
// so switching back to an older binary removes it with the beta runtime.
func startLightpanda(ctx context.Context, proxyAddr, caCert string) *lightpandaSupervisor {
	if os.Getenv("ARTEX_LIGHTPANDA_MCP_URL") != "" {
		_ = os.Unsetenv("ARTEX_LIGHTPANDA_PID")
		return nil // explicitly configured external MCP
	}
	if _, err := exec.LookPath("lightpanda"); err != nil {
		return nil // standalone builds can still opt in with an external URL
	}
	s := &lightpandaSupervisor{ctx: ctx, probe: probeLightpanda}
	s.Update(proxyAddr, caCert)
	return s
}

func probeLightpanda(ctx context.Context) bool {
	client, err := mcphttp.New(ctx, "lightpanda_probe", lightpandaURL, nil, false)
	if err != nil {
		return false
	}
	_ = client.Close()
	return true
}

func (s *lightpandaSupervisor) Update(proxyAddr, caCert string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.proxy == proxyAddr && s.cert == caCert {
		select {
		case <-s.done:
		default:
			return
		}
	}
	if s.cmd != nil {
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	s.cmd = nil
	s.proxy, s.cert = proxyAddr, caCert
	_ = os.Unsetenv("ARTEX_LIGHTPANDA_MCP_URL")
	_ = os.Unsetenv("ARTEX_LIGHTPANDA_PID")
	cmd := exec.CommandContext(s.ctx, "lightpanda", lightpandaArgs(proxyAddr, caCert)...)
	cmd.Env = append(os.Environ(), "LIGHTPANDA_DISABLE_TELEMETRY=true")
	if err := cmd.Start(); err != nil {
		log.Printf("[lightpanda] 启动失败: %v", err)
		return
	}
	s.cmd = cmd
	s.done = make(chan struct{})
	done := s.done
	go func() {
		err := cmd.Wait()
		close(done)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cmd != cmd {
			return
		}
		s.cmd = nil
		if s.ctx.Err() == nil {
			_ = os.Unsetenv("ARTEX_LIGHTPANDA_MCP_URL")
			_ = os.Unsetenv("ARTEX_LIGHTPANDA_PID")
			log.Printf("[lightpanda] 进程退出: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-s.done:
			return
		default:
		}
		probeCtx, cancel := context.WithTimeout(s.ctx, 500*time.Millisecond)
		ready := s.probe(probeCtx)
		cancel()
		if ready {
			_ = os.Setenv("ARTEX_LIGHTPANDA_MCP_URL", lightpandaURL)
			_ = os.Setenv("ARTEX_LIGHTPANDA_PID", strconv.Itoa(cmd.Process.Pid))
			select {
			case <-s.done:
				_ = os.Unsetenv("ARTEX_LIGHTPANDA_MCP_URL")
				_ = os.Unsetenv("ARTEX_LIGHTPANDA_PID")
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
