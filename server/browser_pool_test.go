package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcphttp"
	actool "github.com/Autumn-27/norma/tool"
)

type testBrowserClient struct{ closed chan struct{} }

func (c *testBrowserClient) Tools(context.Context) ([]actool.CoreTool, error) { return nil, nil }
func (c *testBrowserClient) Close() error                                     { close(c.closed); return nil }

type testLightpandaClient struct {
	testBrowserClient
	closedSession string
}

func (c *testLightpandaClient) SessionID() string { return "session-test" }
func (c *testLightpandaClient) CallDefault(_ context.Context, name string, args any) (string, error) {
	if name == "session_close" {
		c.closedSession = args.(map[string]string)["id"]
	}
	return "closed", nil
}

func TestBrowserPoolWaitsAndForceCloseReleasesSlot(t *testing.T) {
	p := newBrowserPool(nil)
	p.setLimit(browserMCPKindChrome, 2)
	first, err := p.acquire(context.Background(), browserMCPKindChrome, "worker", agent.RunInfo{TaskID: 42})
	if err != nil {
		t.Fatal(err)
	}
	client := &testBrowserClient{closed: make(chan struct{})}
	first.client = client
	second, err := p.acquire(context.Background(), browserMCPKindChrome, "planner", agent.RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan *browserLease, 1)
	go func() {
		lease, _ := p.acquire(ctx, browserMCPKindChrome, "reporter", agent.RunInfo{})
		result <- lease
	}()
	deadline := time.Now().Add(time.Second)
	for p.status()[0].Waiting != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := p.status()[0]; got.Active != 2 || got.Waiting != 1 || got.Instances[0].Owner == "" {
		t.Fatalf("unexpected status: %+v", got)
	}
	if ok, err := p.closeID(first.id); !ok || err != nil {
		t.Fatalf("failed to force close active session: %v", err)
	}
	select {
	case <-client.closed:
	default:
		t.Fatal("force close did not close the MCP client")
	}
	select {
	case next := <-result:
		if next == nil {
			t.Fatal("waiter was not admitted")
		}
		defer next.Close()
	case <-ctx.Done():
		t.Fatal("waiter did not resume after force close")
	}
	if ok, _ := p.closeID(first.id); ok {
		t.Fatal("closed session was still active")
	}
}

func TestBrowserPoolWaiterCannotBeBypassed(t *testing.T) {
	p := newBrowserPool(nil)
	p.setLimit(browserMCPKindChrome, 1)
	first, err := p.acquire(context.Background(), browserMCPKindChrome, "first", agent.RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	oldest := make(chan *browserLease, 1)
	go func() {
		lease, _ := p.acquire(waitCtx, browserMCPKindChrome, "oldest", agent.RunInfo{})
		oldest <- lease
	}()
	deadline := time.Now().Add(time.Second)
	for p.status()[0].Waiting != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p.status()[0].Waiting != 1 {
		t.Fatal("first waiter was not queued")
	}
	// The released slot is visible before the awakened waiter runs.
	p.mu.Lock()
	delete(p.active, first.id)
	p.mu.Unlock()
	lateCtx, cancelLate := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelLate()
	late, err := p.acquire(lateCtx, browserMCPKindChrome, "late", agent.RunInfo{})
	if late != nil {
		late.Close()
		t.Fatal("new arrival bypassed the queued waiter")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late arrival should wait, got %v", err)
	}
	p.setLimit(browserMCPKindChrome, 1)
	select {
	case lease := <-oldest:
		if lease == nil {
			t.Fatal("oldest waiter did not get the released slot")
		}
		defer lease.Close()
	case <-waitCtx.Done():
		t.Fatal("oldest waiter never resumed")
	}
}

func TestLightpandaCloseAndExtraSessionLimit(t *testing.T) {
	p := newBrowserPool(nil)
	p.setLimit(browserMCPKindLightpanda, 1)
	lease, err := p.acquire(context.Background(), browserMCPKindLightpanda, "worker", agent.RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	client := &testLightpandaClient{testBrowserClient: testBrowserClient{closed: make(chan struct{})}}
	lease.client = client
	called := false
	tool := actool.Build(actool.Spec{
		Name:   "mcp__lightpanda__session_new",
		Schema: map[string]any{"type": "object"},
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			called = true
			return actool.Text("created"), nil
		},
	})
	result, err := (&browserLeaseTool{CoreTool: tool, lease: lease}).Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || !result.IsError || called {
		t.Fatalf("session_new bypassed limit: result=%+v err=%v", result, err)
	}
	if ok, err := p.closeID(lease.id); !ok || err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if client.closedSession != "session-test" {
		t.Fatal("Lightpanda browser session was not closed")
	}
}

func TestBrowserPoolCanceledWaiter(t *testing.T) {
	p := newBrowserPool(nil)
	p.setLimit(browserMCPKindLightpanda, 1)
	first, _ := p.acquire(context.Background(), browserMCPKindLightpanda, "worker", agent.RunInfo{})
	defer first.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.acquire(ctx, browserMCPKindLightpanda, "worker", agent.RunInfo{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if p.status()[1].Waiting != 0 {
		t.Fatal("canceled waiter remained queued")
	}
}

func TestBrowserPoolCanceledHeadLetsNextWaiterThrough(t *testing.T) {
	p := newBrowserPool(nil)
	p.setLimit(browserMCPKindChrome, 1)
	first, err := p.acquire(context.Background(), browserMCPKindChrome, "first", agent.RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	headCtx, cancelHead := context.WithCancel(context.Background())
	headResult := make(chan error, 1)
	go func() {
		_, err := p.acquire(headCtx, browserMCPKindChrome, "head", agent.RunInfo{})
		headResult <- err
	}()
	deadline := time.Now().Add(time.Second)
	for p.status()[0].Waiting != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	nextCtx, cancelNext := context.WithTimeout(context.Background(), time.Second)
	defer cancelNext()
	nextResult := make(chan *browserLease, 1)
	go func() {
		lease, _ := p.acquire(nextCtx, browserMCPKindChrome, "next", agent.RunInfo{})
		nextResult <- lease
	}()
	for p.status()[0].Waiting != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p.status()[0].Waiting != 2 {
		t.Fatal("both waiters were not queued")
	}
	cancelHead()
	if err := <-headResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled head returned %v", err)
	}
	if p.status()[0].Waiting != 1 {
		t.Fatal("canceled head remained queued")
	}
	first.Close()
	select {
	case lease := <-nextResult:
		if lease == nil {
			t.Fatal("next waiter did not get the slot")
		}
		defer lease.Close()
	case <-nextCtx.Done():
		t.Fatal("next waiter remained blocked after release")
	}
}

func TestBrowserStdioPIDAndMemory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stdio PID sampling uses ps")
	}
	// Tiny local MCP handshake confirms the wrapper records the actual process.
	command := `IFS= read -r line; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{}}'; while IFS= read -r line; do :; done`
	m := &db.MCPServer{Name: browserMCPName, Transport: "stdio", Command: "sh", Args: encodeJSON([]string{"-c", command})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cl, pid, err := connectBrowserMCP(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if pid <= 0 || pid == os.Getpid() {
		t.Fatalf("unexpected browser PID %d", pid)
	}
	if rss := processTreeRSS(readProcessTable(), pid); rss == nil || *rss <= 0 {
		t.Fatalf("browser RSS unavailable: %v", rss)
	}
}

func TestLightpandaSharedMemoryStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("RSS sampling uses ps")
	}
	t.Setenv("ARTEX_LIGHTPANDA_PID", strconv.Itoa(os.Getpid()))
	status := newBrowserPool(nil).status()[1]
	if status.SharedMemory == nil || *status.SharedMemory <= 0 {
		t.Fatalf("shared Lightpanda memory missing: %+v", status)
	}
}

func TestParseProcStatus(t *testing.T) {
	proc, ok := parseProcStatus([]byte("Name:\tlightpanda\nPPid:\t42\nVmRSS:\t1234 kB\n"))
	if !ok || proc.parent != 42 || proc.rss != 1234 {
		t.Fatalf("incorrect Linux process memory: %+v, %v", proc, ok)
	}
}

func TestLightpandaLiveSessionClose(t *testing.T) {
	url := os.Getenv("ARTEX_TEST_LIGHTPANDA_URL")
	if url == "" {
		t.Skip("requires a disposable Lightpanda MCP")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := mcphttp.New(ctx, lightpandaMCPName, url, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID() == "" {
		t.Fatal("Lightpanda did not return a session ID")
	}
	observer, err := mcphttp.New(ctx, lightpandaMCPName, url, nil, false)
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	defer observer.Close()
	before, err := observer.Call(ctx, "session_list", map[string]any{})
	if err != nil || !strings.Contains(before, first.SessionID()) {
		t.Fatalf("new session absent before close: %v", err)
	}
	p := newBrowserPool(nil)
	lease, err := p.acquire(ctx, browserMCPKindLightpanda, "worker", agent.RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	lease.client = first
	if ok, err := p.closeID(lease.id); !ok || err != nil {
		t.Fatalf("Lightpanda close failed: %v", err)
	}
	after, err := observer.Call(ctx, "session_list", map[string]any{})
	if err != nil || strings.Contains(after, first.SessionID()) {
		t.Fatalf("closed session remains in Lightpanda: %v", err)
	}
}
