const assert = require("node:assert/strict");
const test = require("node:test");

require("ts-node").register({
  skipProject: true,
  transpileOnly: true,
  compilerOptions: { target: "ES2022", module: "CommonJS", moduleResolution: "Node" },
});

const { mockHandle } = require("./handler.ts");

test("account security demo responses have the shapes used by the settings page", async () => {
  const sessions = await mockHandle("GET", "/auth/sessions");
  assert.equal(sessions.count, sessions.sessions.length);
  assert.equal(sessions.sessions.filter((item) => item.current).length, 1);
  assert.ok(sessions.sessions.some((item) => !item.current));
  assert.deepEqual(await mockHandle("GET", "/settings/passkeys"), { origin: "", passkeys: [] });
  assert.deepEqual(await mockHandle("GET", "/settings/login-failure-notify"), { enabled: false });
  assert.deepEqual(await mockHandle("GET", "/settings/ip-whitelist"), {
    enabled: false,
    cidrs: [],
    current_ip: "192.0.2.1",
  });
  await assert.rejects(mockHandle("POST", "/auth/sessions/demo-other/revoke", "{}"), /演示模式不支持/);
});
