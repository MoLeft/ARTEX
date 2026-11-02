package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/mcp"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// lazyBrowserClient holds one browser lease only after its first tool call.
type lazyBrowserClient struct {
	mu      sync.Mutex
	ctx     context.Context
	pool    *browserPool
	pg      *db.DB
	server  db.MCPServer
	owner   string
	run     agent.RunInfo
	client  mcpClient
	cancel  context.CancelFunc
	tools   map[string]actool.CoreTool
	cached  map[string]string
	catalog []db.MCPTool
	closed  bool
}

func (p *browserPool) lazyTools(ctx context.Context, pg *db.DB, m *db.MCPServer, owner string, run agent.RunInfo, catalog []db.MCPTool) ([]actool.CoreTool, *lazyBrowserClient) {
	lazy := &lazyBrowserClient{ctx: ctx, pool: p, pg: pg, server: *m, owner: owner, run: run}
	var out []actool.CoreTool
	for _, item := range catalog {
		var schema map[string]any
		if !strings.HasPrefix(item.Name, mcp.ToolName(m.Name, "")) || len(item.Schema) == 0 || json.Unmarshal(item.Schema, &schema) != nil || schema == nil {
			lazy.cached = nil
			return []actool.CoreTool{lazy.dispatchTool()}, lazy
		}
		name := item.Name
		if lazy.cached == nil {
			lazy.cached = make(map[string]string, len(catalog))
		}
		canonical, _ := json.Marshal(schema)
		lazy.cached[name] = string(canonical)
		out = append(out, actool.Build(actool.Spec{
			Name: name, Description: item.Description, Schema: schema,
			Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
				return permission.AskUser("call MCP tool " + name + "?")
			},
			Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
				return lazy.call(ctx, name, in, tc)
			},
		}))
	}
	if len(out) == 0 {
		return []actool.CoreTool{lazy.dispatchTool()}, lazy
	}
	return out, lazy
}

func (l *lazyBrowserClient) dispatchTool() actool.CoreTool {
	name := mcp.ToolName(l.server.Name, "dispatch")
	return actool.Build(actool.Spec{
		Name:        name,
		Description: "按需连接浏览器。先传 tool=__list_tools__ 查看可用工具及参数格式，再传 tool=工具名、arguments=参数调用；只有调用此工具时才占用浏览器名额。",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"tool": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"},
		}, "required": []string{"tool"}},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.AskUser("call MCP tool " + name + "?")
		},
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var req struct {
				Tool      string          `json:"tool"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(in, &req); err != nil || req.Tool == "" {
				return actool.Errorf("请提供浏览器工具名"), nil
			}
			if req.Tool == "__list_tools__" {
				if err := l.connect(ctx); err != nil {
					return actool.Errorf("浏览器连接失败：" + err.Error()), nil
				}
				l.mu.Lock()
				catalog := make([]db.MCPTool, 0, len(l.catalog))
				for _, item := range l.catalog {
					if l.allowed(item.Name) {
						catalog = append(catalog, item)
					}
				}
				l.mu.Unlock()
				raw, _ := json.Marshal(catalog)
				return actool.Text(actool.Capture(tc, string(raw))), nil
			}
			toolName := req.Tool
			if !strings.HasPrefix(toolName, mcp.ToolName(l.server.Name, "")) {
				toolName = mcp.ToolName(l.server.Name, toolName)
			}
			if !l.allowed(toolName) {
				return actool.Errorf("当前对话未启用 Chrome 截图工具"), nil
			}
			return l.call(ctx, toolName, req.Arguments, tc)
		},
	})
}

func (l *lazyBrowserClient) allowed(name string) bool {
	return l.server.Name != browserMCPName || !isBrowserScreenshotTool(name) ||
		(l.owner == "reporter" && l.pg != nil && l.pg.GetBool(settingBrowserScreenshot, false))
}

func (l *lazyBrowserClient) connect(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("浏览器会话已关闭")
	}
	if l.client != nil {
		return nil
	}
	connectCtx, cancel := context.WithCancel(l.ctx)
	stop := context.AfterFunc(ctx, cancel)
	cl, err := l.pool.connect(connectCtx, &l.server, l.owner, l.run)
	if err != nil {
		stop()
		cancel()
		return err
	}
	tools, err := cl.Tools(connectCtx)
	stop()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		cancel()
		_ = cl.Close()
		return err
	}
	l.tools = make(map[string]actool.CoreTool, len(tools))
	l.catalog = make([]db.MCPTool, 0, len(tools))
	for _, t := range tools {
		schema, err := json.Marshal(t.InputSchema())
		if err != nil {
			cancel()
			_ = cl.Close()
			return fmt.Errorf("%s 工具参数格式无效: %w", t.Name(), err)
		}
		l.tools[t.Name()] = t
		l.catalog = append(l.catalog, db.MCPTool{Name: t.Name(), Description: t.Description(), Schema: schema})
	}
	l.client, l.cancel = cl, cancel
	if l.pg != nil {
		if err := l.pg.SaveMCPTools(l.server.ID, l.catalog); err != nil {
			log.Printf("[mcp] %s 更新浏览器工具缓存失败: %v", l.server.Name, err)
		}
	}
	return nil
}

func (l *lazyBrowserClient) call(ctx context.Context, name string, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	if err := l.connect(ctx); err != nil {
		return actool.Errorf("浏览器连接失败：" + err.Error()), nil
	}
	l.mu.Lock()
	tool := l.tools[name]
	cached, hadCache := l.cached[name]
	l.mu.Unlock()
	if tool == nil {
		return actool.Errorf("当前浏览器未提供工具 " + name + "；请刷新 MCP 工具清单后重试"), nil
	}
	if hadCache {
		actual, err := json.Marshal(tool.InputSchema())
		if err != nil || string(actual) != cached {
			return actool.Errorf("浏览器工具参数格式已更新；请在下一轮对话重试，或手动刷新 MCP 工具清单"), nil
		}
	}
	return tool.Call(ctx, in, tc)
}

func (l *lazyBrowserClient) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	cl, cancel := l.client, l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cl != nil {
		return cl.Close()
	}
	return nil
}
