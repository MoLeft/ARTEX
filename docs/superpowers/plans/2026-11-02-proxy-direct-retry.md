# Proxy Direct Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in direct retry when the configured global proxy cannot be connected, without pausing tasks or retrying target-side failures.

**Architecture:** A loopback HTTP egress proxy owns the global upstream connection and optional one-time direct retry. With capture enabled, the recording MITM forwards to this egress proxy; with capture disabled, ARTEX-managed clients use it directly. The settings API and browser launchers read the same persisted flag and route.

**Tech Stack:** Go 1.26, standard `net/http`/`net`, existing `golang.org/x/net` for SOCKS5 where stage attribution is reliable, PostgreSQL settings, Next.js/React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-11-02-proxy-direct-retry-design.md`

## Global Constraints

- `proxy_direct_retry_on_connect_failure` defaults to `false`.
- Only a proven upstream proxy connection, handshake, or authentication failure before target delivery may retry directly, once.
- A blocked/refused target, HTTP status, certificate error, or failure after target delivery never retries directly.
- Disabled retry fails only the current request; no proxy error automatically pauses a task.
- Capture-on requests remain recorded; capture-off requests are not recorded.
- LLM/search/update proxies, explicit custom-tool proxies, and externally managed MCPs stay independent.
- The egress listener binds only to loopback; fallback logs contain no credentials, full URL, or response body.

## Review Focus

1. SOCKS5 returns a target-unreachable reply while its server is healthy: fail the request without direct retry (Task 1 test).
2. An HTTP POST body has already been sent or cannot be replayed: fail instead of duplicating the request (Task 1 test).
3. `Proxy-Authorization` from the upstream path: never forward it to the target on direct retry (Task 1 test).
4. Capture mode changes while a browser session exists: new sessions take the new route; existing Lightpanda sessions are closed on restart (Task 3 test).
5. A historical `paused_by_user` or scheduled task exists at upgrade: startup migration never admits it (Task 4 test).

---

### Task 1: Loopback egress proxy and exact retry boundary

**Files:**
- Create: `traffic/egress_proxy.go`
- Create: `traffic/egress_proxy_test.go`
- Reuse: `traffic/traffic.go` proxy URL validation

**Interfaces:**
- Produces: `func OpenEgressProxy() (*EgressProxy, error)`, `func (p *EgressProxy) Addr() string`, `func (p *EgressProxy) SetUpstream(raw string, directRetry bool) error`, `func (p *EgressProxy) Close() error`.
- `Addr` returns an `http://127.0.0.1:<port>` URL; `SetUpstream` atomically changes settings for new requests while existing tunnels finish.

- [ ] **Step 1: Write failing tests** in `traffic/egress_proxy_test.go`: `TestEgressProxyFallback` asserts a refused proxy endpoint reaches the local target exactly once only when enabled, while the same request fails when disabled; run the cases for HTTP and CONNECT.
- [ ] **Step 2: Verify failure.** Run `go test ./traffic -run TestEgressProxyFallback -count=1`; expect compile/test failure before implementation.
- [ ] **Step 3: Implement** the interface above, with a loopback HTTP listener, HTTP forwarding and CONNECT tunneling. Establish the upstream route before streaming the target request body; tag errors by actual proxy DNS/TCP/TLS/handshake/auth stage rather than matching error strings. For SOCKS5 distinguish proxy negotiation/auth from its target-connect reply. Retry direct only when the route proves no target delivery; if a request body cannot be replayed, fail safely.
- [ ] **Step 4: Add boundary tests**: `TestEgressProxyTargetFailureDoesNotFallback`, `TestEgressProxyPostDoesNotDuplicate`, `TestEgressProxyStripsProxyAuthorization`, `TestEgressProxySocks5FailureNoPanic`, `TestEgressProxyUpstreamSchemes`, and `TestEgressProxyConfigSwapKeepsOpenTunnel`. Use local fake proxies and targets; cover HTTP/HTTPS/SOCKS5 proxy handshake/auth failures, a SOCKS5 target-unreachable reply, no target access on target-side errors, one target access for safe fallback, no credential header on direct delivery, and no process panic.
- [ ] **Step 5: Verify and commit.** Run `go test ./traffic -count=1` and `go test -race ./traffic -run 'TestEgressProxy' -count=1`; expect PASS. Commit `traffic/egress_proxy.go` and its test.

### Task 2: Persist the flag and route ARTEX target clients

**Files:**
- Modify: `server/manager.go` (load/apply setting, own/close egress proxy, `ProxyAddr`, capture mode, browser proxy sync)
- Modify: `server/server.go` (GET/PUT settings)
- Modify: `server/customtool.go`, `server/repeater.go` (global/recording proxy selections)
- Modify: `server/egress_probe.go` (manual test uses saved upstream, never the fallback route)
- Create: `server/proxy_direct_retry_test.go`

**Interfaces:**
- Consumes: Task 1 `OpenEgressProxy`, `Addr`, `SetUpstream`, `Close`.
- Produces: `func (m *Manager) SetProxyDirectRetry(on bool) error`, `func (m *Manager) ProxyDirectRetry() bool`; `Manager.ProxyAddr()` returns the local egress URL when capture is off and a global proxy exists, the recording proxy URL when capture is on, and direct when neither applies.

- [ ] **Step 1: Write failing tests**: `TestProxyDirectRetrySettingDefaultsOffAndPersists` checks missing DB key is false and GET/PUT round-trips; `TestProxyRouteByCaptureMode` checks the four rows in the spec's request-path table; `TestGlobalHTTPToolsUseEgressProxy` checks only global/recording proxy selections use the new route.
- [ ] **Step 2: Verify failure.** Run `go test ./server -run 'TestProxyDirectRetrySetting|TestProxyRouteByCaptureMode|TestGlobalHTTPToolsUseEgressProxy' -count=1`; expect FAIL.
- [ ] **Step 3: Implement** the two Manager methods and settings API. Start/close one egress proxy with Manager, feed it the saved global URL and flag, point the MITM upstream at its loopback URL only when a global proxy exists, and route custom HTTP/repeater requests that opt into global or recording proxy through the same entry. Keep explicitly configured other proxies unchanged. Preserve existing validation and browser sync on settings changes.
- [ ] **Step 4: Verify and commit.** Run the Task 2 tests and `go test ./server -run 'TestEgress|TestRepeaterProxy|TestProxy' -count=1`; expect PASS. Commit the touched server files and test.

### Task 3: Route managed Chrome and Lightpanda through the same entry

**Files:**
- Modify: `cmd/artex/main.go`, `cmd/artex/lightpanda.go`
- Modify: `server/server.go` (notify managed Lightpanda of address/CA changes)
- Modify: `server/manager.go` (Chrome proxy address/CA sync)
- Create: `cmd/artex/lightpanda_test.go`
- Modify: `server/browser_mcp_test.go`

**Interfaces:**
- Consumes: Task 2 `Manager.ProxyAddr()` and `Manager.ProxyCACert()`.
- Produces: `func startLightpanda(ctx context.Context, proxyAddr, caCert string) *lightpandaSupervisor` and `func (s *lightpandaSupervisor) Update(proxyAddr, caCert string)`; `func (s *Server) SetLightpandaProxyUpdater(fn func(string, string))` stores the callback used after settings changes.

- [ ] **Step 1: Write failing tests**: `TestLightpandaProxyArgs` asserts `--http-proxy` and capture CA are passed when needed; `TestLightpandaProxyUpdate` uses a temporary fake `lightpanda` executable on `PATH` and asserts no restart for an unchanged address/CA and one restart for a changed route; extend the Chrome test to assert the managed local route in both capture modes.
- [ ] **Step 2: Verify failure.** Run `go test ./cmd/artex ./server -run 'TestLightpandaProxy|TestBrowserMCP' -count=1`; expect FAIL.
- [ ] **Step 3: Implement** the interfaces above. Construct Manager before starting bundled Lightpanda; preserve the external-MCP branch as unmanaged. Use the installed Lightpanda `--http-proxy` and `--ca-cert` flags. Trigger `Update` only when effective address/CA changes, and let the existing Chrome MCP proxy sync handle Chrome.
- [ ] **Step 4: Verify and commit.** Run the Task 3 tests and `go test ./cmd/artex ./server -run 'TestLightpanda|TestBrowserMCP' -count=1`; expect PASS. Commit the touched files.

### Task 4: Stop proxy-triggered task pauses and migrate old paused tasks

**Files:**
- Modify: `server/server.go`, `server/engine.go`, `server/customtool.go`, `server/proxy_autoresume.go`
- Modify: `server/proxy_autoresume_test.go`, `server/egress_probe_test.go`

**Interfaces:**
- Consumes: existing `proxyReconnectSuccessWithGuards` and schedule/admission guards for one-time migration.
- Produces: no new public API; network request failures remain request failures and cannot call `pauseProxyTask`.

- [ ] **Step 1: Write failing tests**: `TestProxyRequestFailureNeverPausesTasks` sends an EOF and a proxy connection failure through the existing Engine/MITM/custom-tool paths and asserts no task acquires `paused_by_proxy`; `TestLegacyProxyPauseStartupRecovery` asserts only historical proxy-paused tasks resume under schedule guards; `TestManualProxyTestNeverFallsBack` asserts the test endpoint reports the saved upstream's failure even when direct retry is enabled.
- [ ] **Step 2: Verify failure.** Run `go test ./server -run 'TestProxyRequestFailureNeverPausesTasks|TestLegacyProxyPauseStartupRecovery|TestManualProxyTestNeverFallsBack' -count=1`; expect FAIL.
- [ ] **Step 3: Remove** all Engine/MITM/custom-tool proxy-failure pause call sites and the unused reconnect supervisor/classifier code; retain only guarded legacy recovery and the manual proxy test. After `restoreTaskRuntimes`, call the guarded recovery once so old proxy-paused tasks become eligible without reviving user/schedule/terminal pauses. Keep old setting keys inert for database compatibility.
- [ ] **Step 4: Verify and commit.** Run Task 4 tests, then `go test ./server -count=1`; expect PASS. Commit the touched files.

### Task 5: Expose the safe default and finish cross-mode verification

**Files:**
- Modify: `web/src/lib/types.ts`, `web/src/lib/mock/data.ts`, `web/src/app/(main)/system/settings/page.tsx`
- Modify: relevant proxy description in `web/src/app/(main)/system/settings/page.tsx`

**Interfaces:**
- Consumes: Task 2 GET/PUT `proxy_direct_retry_on_connect_failure` and Task 4 removal of active reconnect behavior.
- Produces: one default-off switch with a source-IP disclosure; no visible reconnection controls.

- [ ] **Step 1: Add** the settings type/mock field and the global-proxy switch. Saving uses the existing `api.setSettings` path; show that direct retry can expose the host exit IP and that bundled Lightpanda sessions restart when their proxy route changes. Remove the now-inert reconnect inputs/help text from the page.
- [ ] **Step 2: Verify frontend.** Run `cd web && npx tsc --noEmit && npx biome check 'src/app/(main)/system/settings/page.tsx' src/lib/types.ts src/lib/mock/data.ts`; expect zero errors.
- [ ] **Step 3: Verify full backend.** Run `go test ./traffic ./server ./cmd/artex -count=1`; expect PASS. Run a local smoke request through both capture modes with a local refused proxy and local target; assert fallback only when enabled and no task pause.
- [ ] **Step 4: Commit** the frontend files and any test-only corrections. Review the branch diff against the spec, confirm no unrelated settings/proxies changed, then prepare the requested GitHub/main/Mac Mini delivery using the existing deployment workflow.
