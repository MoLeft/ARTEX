package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const (
	settingBrowserChromeMax     = "browser_chrome_max_instances"
	settingBrowserLightpandaMax = "browser_lightpanda_max_instances"
)

func browserLimitKey(kind string) string {
	if kind == browserMCPKindLightpanda {
		return settingBrowserLightpandaMax
	}
	return settingBrowserChromeMax
}

func browserKind(name string) string {
	switch name {
	case browserMCPName:
		return browserMCPKindChrome
	case lightpandaMCPName:
		return browserMCPKindLightpanda
	default:
		return ""
	}
}

type browserPool struct {
	mu      sync.Mutex
	limits  map[string]int
	nextID  int64
	active  map[int64]*browserLease
	waiting map[string]int
	wake    chan struct{}
}

type browserLease struct {
	pool      *browserPool
	id        int64
	kind      string
	owner     string
	run       agent.RunInfo
	pid       int
	client    mcpClient
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func newBrowserPool(pg *db.DB) *browserPool {
	p := &browserPool{limits: make(map[string]int), active: make(map[int64]*browserLease), waiting: make(map[string]int), wake: make(chan struct{})}
	if pg != nil {
		for _, kind := range []string{browserMCPKindChrome, browserMCPKindLightpanda} {
			if raw, ok, err := pg.GetSetting(browserLimitKey(kind)); err == nil && ok {
				if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 100 {
					p.limits[kind] = n
				}
			}
		}
	}
	return p
}

func (p *browserPool) limit(kind string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.limits[kind]
}

func (p *browserPool) setLimit(kind string, limit int) {
	p.mu.Lock()
	p.limits[kind] = limit
	close(p.wake)
	p.wake = make(chan struct{})
	p.mu.Unlock()
}

func (p *browserPool) acquire(ctx context.Context, kind, owner string, run agent.RunInfo) (*browserLease, error) {
	queued := false
	defer func() {
		if queued {
			p.mu.Lock()
			p.waiting[kind]--
			p.mu.Unlock()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		limit := p.limits[kind]
		count := 0
		for _, lease := range p.active {
			if lease.kind == kind {
				count++
			}
		}
		if limit == 0 || count < limit {
			p.nextID++
			connectCtx, cancel := context.WithCancel(ctx)
			lease := &browserLease{pool: p, id: p.nextID, kind: kind, owner: owner, run: run, ctx: connectCtx, cancel: cancel}
			p.active[lease.id] = lease
			p.mu.Unlock()
			return lease, nil
		}
		if !queued {
			p.waiting[kind]++
			queued = true
		}
		wake := p.wake
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func (p *browserPool) connect(ctx context.Context, m *db.MCPServer, owner string, run agent.RunInfo) (mcpClient, error) {
	kind := browserKind(m.Name)
	if kind == "" {
		return connectMCP(ctx, m)
	}
	lease, err := p.acquire(ctx, kind, owner, run)
	if err != nil {
		return nil, err
	}
	cl, pid, err := connectBrowserMCP(lease.ctx, m)
	if err != nil {
		lease.Close()
		return nil, err
	}
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		_ = cl.Close()
		return nil, errors.New("浏览器会话已被关闭")
	}
	lease.client, lease.pid = cl, pid
	lease.mu.Unlock()
	return lease, nil
}

func (l *browserLease) Tools(ctx context.Context) ([]actool.CoreTool, error) {
	l.mu.Lock()
	cl := l.client
	closed := l.closed
	l.mu.Unlock()
	if closed || cl == nil {
		return nil, errors.New("浏览器会话已被关闭")
	}
	tools, err := cl.Tools(ctx)
	if err != nil {
		return nil, err
	}
	for i, tool := range tools {
		tools[i] = &browserLeaseTool{CoreTool: tool, lease: l}
	}
	return tools, nil
}

type browserLeaseTool struct {
	actool.CoreTool
	lease *browserLease
}

func (t *browserLeaseTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	if t.lease.kind == browserMCPKindLightpanda && strings.HasSuffix(t.Name(), "__session_new") && t.lease.pool.limit(browserMCPKindLightpanda) > 0 {
		return actool.Errorf("已设置 Lightpanda 会话上限，不能额外创建会话"), nil
	}
	if t.lease.ctx.Err() != nil {
		return actool.Errorf("浏览器会话已结束，请重新开始运行"), nil
	}
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.lease.ctx, cancel)
	defer stop()
	defer cancel()
	return t.CoreTool.Call(callCtx, in, tc)
}

func (l *browserLease) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		cl := l.client
		l.mu.Unlock()
		l.cancel()
		if cl != nil {
			if l.kind == browserMCPKindLightpanda {
				if httpClient, ok := cl.(interface {
					SessionID() string
					CallDefault(context.Context, string, any) (string, error)
				}); ok {
					if id := httpClient.SessionID(); id != "" {
						closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						_, l.closeErr = httpClient.CallDefault(closeCtx, "session_close", map[string]string{"id": id})
						cancel()
					} else {
						l.closeErr = errors.New("Lightpanda 未返回会话 ID，无法确认浏览器内存已释放")
					}
				}
			}
			l.closeErr = errors.Join(l.closeErr, cl.Close())
		}
		l.pool.mu.Lock()
		delete(l.pool.active, l.id)
		close(l.pool.wake)
		l.pool.wake = make(chan struct{})
		l.pool.mu.Unlock()
	})
	return l.closeErr
}

func (p *browserPool) closeID(id int64) (bool, error) {
	p.mu.Lock()
	lease := p.active[id]
	p.mu.Unlock()
	if lease == nil {
		return false, nil
	}
	return true, lease.Close()
}

type browserInstance struct {
	ID        int64  `json:"id"`
	Owner     string `json:"owner"`
	TaskID    int64  `json:"task_id,omitempty"`
	IntentID  int64  `json:"intent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Memory    *int64 `json:"memory_bytes"`
}

type browserPoolStatus struct {
	Kind         string            `json:"kind"`
	Limit        int               `json:"limit"`
	Active       int               `json:"active"`
	Waiting      int               `json:"waiting"`
	SharedMemory *int64            `json:"shared_memory_bytes"`
	Instances    []browserInstance `json:"instances"`
}

func (p *browserPool) status() []browserPoolStatus {
	p.mu.Lock()
	rows := make(map[string][]browserLease)
	for _, l := range p.active {
		l.mu.Lock()
		rows[l.kind] = append(rows[l.kind], browserLease{id: l.id, owner: l.owner, run: l.run, pid: l.pid})
		l.mu.Unlock()
	}
	waiting := map[string]int{browserMCPKindChrome: p.waiting[browserMCPKindChrome], browserMCPKindLightpanda: p.waiting[browserMCPKindLightpanda]}
	limits := map[string]int{browserMCPKindChrome: p.limits[browserMCPKindChrome], browserMCPKindLightpanda: p.limits[browserMCPKindLightpanda]}
	p.mu.Unlock()
	var shared *int64
	sharedPID, _ := strconv.Atoi(os.Getenv("ARTEX_LIGHTPANDA_PID"))
	needProcesses := sharedPID > 0
	for _, leases := range rows {
		for _, l := range leases {
			needProcesses = needProcesses || l.pid > 0
		}
	}
	var processes map[int]browserProcess
	if needProcesses {
		processes = readProcessTable()
	}
	if sharedPID > 0 {
		shared = processTreeRSS(processes, sharedPID)
	}
	var out []browserPoolStatus
	for _, kind := range []string{browserMCPKindChrome, browserMCPKindLightpanda} {
		instances := make([]browserInstance, 0, len(rows[kind]))
		for _, l := range rows[kind] {
			var mem *int64
			if l.pid > 0 {
				mem = processTreeRSS(processes, l.pid)
			}
			instances = append(instances, browserInstance{ID: l.id, Owner: l.owner, TaskID: l.run.TaskID, IntentID: l.run.IntentID, SessionID: l.run.SessionID, Memory: mem})
		}
		sort.Slice(instances, func(i, j int) bool { return instances[i].ID < instances[j].ID })
		out = append(out, browserPoolStatus{Kind: kind, Limit: limits[kind], Active: len(instances), Waiting: waiting[kind], SharedMemory: func() *int64 {
			if kind == browserMCPKindLightpanda {
				return shared
			}
			return nil
		}(), Instances: instances})
	}
	return out
}

// The stdio SDK owns its child PID. A shell exec records that same PID without
// changing the server command, allowing the UI to sample the Chrome process tree.
func connectBrowserMCP(ctx context.Context, m *db.MCPServer) (mcpClient, int, error) {
	if m.Transport != "stdio" || runtime.GOOS == "windows" || m.Command == "" {
		cl, err := connectMCP(ctx, m)
		return cl, 0, err
	}
	f, err := os.CreateTemp("", "artex-browser-pid-*")
	if err != nil {
		return nil, 0, err
	}
	path := f.Name()
	_ = f.Close()
	defer os.Remove(path)
	args := append([]string{"-c", `printf '%s' "$$" > "$1"; shift; exec "$@"`, "sh", path, m.Command}, jsonStrSlice(m.Args)...)
	wrapped := *m
	wrapped.Command = "sh"
	wrapped.Args = encodeJSON(args)
	cl, err := connectMCP(ctx, &wrapped)
	if err != nil {
		return nil, 0, err
	}
	raw, _ := os.ReadFile(path)
	pid, _ := strconv.Atoi(string(raw))
	return cl, pid, nil
}

// RSS is an approximate total: shared pages may be counted in multiple child
// processes. Unknown or inaccessible processes are returned as null.
type browserProcess struct {
	parent int
	rss    int64
}

func readProcessTable() map[int]browserProcess {
	if runtime.GOOS == "windows" {
		return nil
	}
	if runtime.GOOS == "linux" {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return nil
		}
		all := map[int]browserProcess{}
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			status, err := os.ReadFile("/proc/" + entry.Name() + "/status")
			if err != nil {
				continue
			}
			if proc, ok := parseProcStatus(status); ok {
				all[pid] = proc
			}
		}
		return all
	}
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=").Output()
	if err != nil {
		return nil
	}
	all := map[int]browserProcess{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		rss, e3 := strconv.ParseInt(fields[2], 10, 64)
		if e1 == nil && e2 == nil && e3 == nil {
			all[pid] = browserProcess{parent, rss}
		}
	}
	return all
}

func parseProcStatus(status []byte) (browserProcess, bool) {
	var proc browserProcess
	parentFound, rssFound := false, false
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "PPid:":
			proc.parent, _ = strconv.Atoi(fields[1])
			parentFound = true
		case "VmRSS:":
			proc.rss, _ = strconv.ParseInt(fields[1], 10, 64)
			rssFound = true
		}
	}
	return proc, parentFound && rssFound
}

func processTreeRSS(all map[int]browserProcess, root int) *int64 {
	if _, ok := all[root]; !ok {
		return nil
	}
	seen := map[int]bool{root: true}
	for changed := true; changed; {
		changed = false
		for pid, proc := range all {
			if !seen[pid] && seen[proc.parent] {
				seen[pid] = true
				changed = true
			}
		}
	}
	var bytes int64
	for pid := range seen {
		bytes += all[pid].rss * 1024
	}
	return &bytes
}
