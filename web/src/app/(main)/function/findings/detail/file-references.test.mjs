import assert from "node:assert/strict";
import { test } from "node:test";

import { fileIndex, resolveFileReference, taskBrowserPath } from "./file-references.ts";

test("finding links only resolve existing files in the matching task", () => {
  const entries = [
    { name: "proof.txt", path: "tasks/7/i1/proof.txt", dir: false },
    { name: "same.log", path: "tasks/7/i1/same.log", dir: false },
    { name: "same.log", path: "tasks/7/i2/same.log", dir: false },
    { name: "foreign.txt", path: "tasks/8/foreign.txt", dir: false },
    { name: "folder", path: "tasks/7/folder", dir: true },
  ];
  const paths = fileIndex(entries, "7", "/app/data/tasks/7");
  const resolve = (value) => resolveFileReference(value, paths);
  assert.equal(resolve("proof.txt"), "tasks/7/i1/proof.txt");
  assert.equal(resolve("i1/proof.txt"), "tasks/7/i1/proof.txt");
  assert.equal(resolve("/app/data/tasks/7/i1/proof.txt"), "tasks/7/i1/proof.txt");
  assert.equal(resolve("workspace://tasks/7/i1/proof.txt"), "tasks/7/i1/proof.txt");
  assert.equal(resolve("same.log"), null);
  assert.equal(resolve("missing.txt"), null);
  assert.equal(resolve("tasks/8/foreign.txt"), null);
  assert.equal(resolve("i1/../i1/proof.txt"), null);
  assert.equal(resolve("https://example.test/proof.txt"), null);
  assert.equal(resolve("/outside/tasks/7/i1/proof.txt"), null);
  assert.equal(resolve("folder"), null);
});

test("sibling task files require an explicit task path", () => {
  const sibling = fileIndex(
    [{ name: "proof.txt", path: "tasks/8/i2/proof.txt", dir: false }],
    "8",
    "/app/data/tasks/8",
    false,
  );
  assert.equal(resolveFileReference("tasks/8/i2/proof.txt", sibling), "tasks/8/i2/proof.txt");
  assert.equal(resolveFileReference("/app/data/tasks/8/i2/proof.txt", sibling), "tasks/8/i2/proof.txt");
  assert.equal(resolveFileReference("proof.txt", sibling), null);
  assert.equal(resolveFileReference("i2/proof.txt", sibling), null);
});

test("file browser stops at the tasks directory", () => {
  for (const path of ["tasks", "tasks/7", "tasks/8/i2/proof.txt"]) assert.equal(taskBrowserPath(path), true);
  for (const path of ["", "transcripts", "tasks/0", "tasks/7/../8", "tasks/7//file", "tasks/7/./file"])
    assert.equal(taskBrowserPath(path), false);
});
