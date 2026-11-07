type TreeEntry = { path: string; name: string };

export function pathState(path: string, selected: ReadonlySet<string>): boolean | "indeterminate" {
  for (const item of selected) if (path === item || path.startsWith(`${item}/`)) return true;
  for (const item of selected) if (item.startsWith(`${path}/`)) return "indeterminate";
  return false;
}

export function groupPathsState(paths: string[], selected: ReadonlySet<string>): boolean | "indeterminate" {
  if (!paths.length) return false;
  if (paths.every((path) => selected.has(path))) return true;
  return paths.some((path) => pathState(path, selected) !== false) ? "indeterminate" : false;
}

export function toggleGroupPaths(paths: string[], selected: ReadonlySet<string>): Set<string> {
  const all = paths.every((path) => selected.has(path));
  const next = new Set(selected);
  for (const item of next) if (paths.some((path) => item === path || item.startsWith(`${path}/`))) next.delete(item);
  if (!all) for (const path of paths) next.add(path);
  return next;
}

export function toggleTreePath(path: string, selected: ReadonlySet<string>, entriesByPath: Record<string, readonly TreeEntry[]>, protectedPaths: string[]): Set<string> {
  const next = new Set(selected);
  const ancestor = [...selected].filter((item) => path === item || path.startsWith(`${item}/`)).sort((a, b) => b.length - a.length)[0];
  if (ancestor === path) {
    next.delete(path);
  } else if (ancestor) {
    const siblings: string[] = [];
    let parent = ancestor;
    while (parent !== path) {
      const entries = entriesByPath[parent];
      if (!entries) return next;
      const child = `${parent}/${path.slice(parent.length + 1).split("/")[0]}`;
      for (const entry of entries) {
        if (entry.path !== child && !protectedPaths.some((locked) => entry.path === locked || entry.path.startsWith(`${locked}/`))) siblings.push(entry.path);
      }
      parent = child;
    }
    next.delete(ancestor);
    for (const sibling of siblings) next.add(sibling);
  } else {
    for (const item of next) if (item.startsWith(`${path}/`)) next.delete(item);
    next.add(path);
  }
  return next;
}
