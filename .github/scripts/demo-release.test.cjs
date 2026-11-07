"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { parseTag, selectLatestPreview, requireCompleteRelease } = require("./demo-release.cjs");

function release(tag, overrides = {}) {
  const version = tag.slice(1);
  return {
    tag_name: tag, draft: false, published_at: "2026-11-07T12:00:00Z",
    assets: ["linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64"]
      .map(platform => ({ name: `artex-${version}-${platform}.zip` })).concat({ name: "SHA256SUMS" }),
    ...overrides,
  };
}

test("accepts valid releases and rejects malformed dates or tags", () => {
  assert.ok(parseTag("v0.26.11.07.003-beta"));
  assert.ok(parseTag("v0.26.11.07.003-beta1"));
  assert.ok(parseTag("v0.26.11.07.003"));
  for (const tag of ["v0.26.11.31.001", "v0.26.02.29.001", "v0.26.11.07.000", "v0.26.11.07.003-rc", "v0.26.11.07.003-beta0"]) {
    assert.equal(parseTag(tag), null, tag);
  }
  assert.ok(parseTag("v0.24.02.29.001"));
});

test("new beta supersedes older formal and beta releases", () => {
  const old = release("v0.26.11.06.002-beta");
  const current = release("v0.26.11.07.003-beta");
  assert.equal(selectLatestPreview([current, old, release("v0.26.11.02.009")]), current);
  assert.equal(selectLatestPreview([old, release("v0.26.11.02.009")]).tag_name, old.tag_name);
});

test("orders beta numbers numerically and formal after beta at the same base", () => {
  assert.equal(selectLatestPreview([release("v0.26.11.07.003-beta2"), release("v0.26.11.07.003-beta10")]).tag_name, "v0.26.11.07.003-beta10");
  assert.equal(selectLatestPreview([release("v0.26.11.07.003-beta10"), release("v0.26.11.07.003")]).tag_name, "v0.26.11.07.003");
  assert.equal(selectLatestPreview([release("v0.26.11.07.003-beta"), release("v0.26.11.08.001")]).tag_name, "v0.26.11.08.001");
});

test("ignores drafts and unrelated tags, rejects incomplete newest release", () => {
  const valid = release("v0.26.11.07.003-beta");
  assert.equal(selectLatestPreview([release("v0.26.11.09.001", { draft: true }), release("v0.26.11.10.001", { published_at: null }), release("other"), valid]), valid);
  const incomplete = release("v0.26.11.08.001", { assets: [] });
  assert.equal(selectLatestPreview([valid, incomplete]), incomplete);
  assert.throws(() => requireCompleteRelease(incomplete), /缺少/);
  assert.equal(requireCompleteRelease(valid), valid);
  assert.throws(() => requireCompleteRelease(null), /没有已发布/);
});
