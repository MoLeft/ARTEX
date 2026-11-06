const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");
const ts = require("typescript");

const compiled = ts.transpileModule(fs.readFileSync(path.join(__dirname, "api.ts"), "utf8"), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;

test("version request uses admin token and bypasses response caches", async () => {
  const exports = {};
  vm.runInNewContext(compiled, {
    exports,
    process: { env: {} },
    URLSearchParams,
    window: {},
    localStorage: { getItem: () => "test-admin-token" },
    require: (id) => {
      if (id === "@/lib/mock/enabled") return { MOCK: false };
      if (id === "@/lib/mock/handler") return { mockHandle: () => assert.fail("unexpected demo request") };
      throw new Error(`Unexpected runtime import: ${id}`);
    },
    fetch: async (url, init) => {
      assert.equal(url, "/api/version");
      assert.equal(init.headers.Authorization, "Bearer test-admin-token");
      assert.equal(init.cache, "no-store");
      return { ok: true, status: 200, json: async () => ({ version: "v1.0.1" }) };
    },
  });
  assert.equal((await exports.api.version()).version, "v1.0.1");
});
