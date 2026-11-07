const assert = require("node:assert/strict");
const test = require("node:test");

require("ts-node").register({
  skipProject: true,
  transpileOnly: true,
  compilerOptions: { target: "ES2022", module: "CommonJS", moduleResolution: "Node" },
});

const { mockHandle } = require("./handler.ts");

test("asset preview supports company pages and scoped tabs", async () => {
  const names = await mockHandle("GET", "/companies?names=1");
  assert.ok(names.some((company) => company.name === "Acme Corp"));

  const page = await mockHandle("GET", "/companies?limit=50&offset=0&search=acme");
  assert.equal(page.total, 1);
  assert.equal(page.companies[0].scope_count, 3);
  assert.equal(page.companies[0].scope.length, 2);

  const detail = await mockHandle("GET", "/companies/1");
  assert.equal(detail.scope.length, 3);

  const counts = await mockHandle("GET", "/assets/counts?company_id=1");
  const assets = await mockHandle("GET", "/assets?type=root_domain&limit=50&offset=0&company_id=1");
  assert.equal(counts.root_domain, assets.total);
  assert.ok(assets.assets.every((asset) => asset.company_id === 1));
  assert.deepEqual(await mockHandle("GET", "/assets/counts?company_id=999"), {});
});
