import assert from "node:assert/strict";
import test from "node:test";
import { groupPathsState, pathState, toggleGroupPaths, toggleTreePath } from "../src/app/(main)/system/settings/_components/storage-cleanup-selection.ts";

test("deselecting one file keeps clean siblings selected", () => {
  const root = "tasks/3/worker";
  const entries = {
    [root]: [{ path: `${root}/nested`, name: "nested" }, { path: `${root}/other.txt`, name: "other.txt" }],
    [`${root}/nested`]: [{ path: `${root}/nested/one.txt`, name: "one.txt" }, { path: `${root}/nested/two.txt`, name: "two.txt" }, { path: `${root}/nested/proof.txt`, name: "proof.txt" }],
  };
  const selected = toggleTreePath(`${root}/nested/one.txt`, new Set([root]), entries, [`${root}/nested/proof.txt`]);
  assert.deepEqual([...selected].sort(), [`${root}/nested/two.txt`, `${root}/other.txt`].sort());
  assert.equal(pathState(root, selected), "indeterminate");
  assert.equal(groupPathsState([root, "tasks/3/cmd-output"], selected), "indeterminate");
  assert.equal(pathState(`${root}/nested/one.txt`, selected), false);
  assert.equal(pathState(`${root}/nested/two.txt`, selected), true);
});

test("task-level selection replaces partial file choices", () => {
  const paths = ["tasks/3/worker", "tasks/3/cmd-output"];
  const partial = new Set(["tasks/3/worker/file.txt"]);
  const all = toggleGroupPaths(paths, partial);
  assert.deepEqual([...all].sort(), paths.sort());
  assert.equal(groupPathsState(paths, all), true);
  assert.equal(toggleGroupPaths(paths, all).size, 0);
});

test("selecting a directory absorbs its selected children", () => {
  const root = "tasks/3/worker";
  const file = `${root}/nested/one.txt`;
  const partial = toggleTreePath(file, new Set(), {}, []);
  assert.equal(pathState(root, partial), "indeterminate");
  assert.deepEqual([...toggleTreePath(`${root}/nested`, partial, {}, [])], [`${root}/nested`]);
});
