import type { WorkspaceEntry } from "@/lib/types";

export function fileIndex(entries: WorkspaceEntry[], taskId: string, absoluteRoot: string, aliases = true) {
  const root = `tasks/${taskId}/`;
  const paths = new Map<string, string | null>();
  for (const file of entries) {
    if (file.dir || !file.path.startsWith(root)) continue;
    paths.set(file.path, file.path);
    paths.set(`${absoluteRoot}/${file.path.slice(root.length)}`, file.path);
    if (!aliases) continue;
    paths.set(file.path.slice(root.length), file.path);
    const previous = paths.get(file.name);
    if (previous === undefined) paths.set(file.name, file.path);
    else if (previous !== file.path) paths.set(file.name, null);
  }
  return paths;
}

export function resolveFileReference(raw: string, paths: Map<string, string | null>) {
  const value = raw
    .trim()
    .replace(/^workspace:\/\//, "")
    .replace(/^\.\//, "");
  if (!value || value.split("/").includes("..") || value.includes("://")) return null;
  return paths.get(value) ?? null;
}

export function taskBrowserPath(path: string) {
  const parts = path.split("/");
  return (
    parts[0] === "tasks" &&
    (parts.length === 1 ||
      (/^(?:[1-9]\d*|t-[a-z0-9-]+)$/.test(parts[1]) &&
        parts.slice(2).every((part) => part && part !== "." && part !== "..")))
  );
}
