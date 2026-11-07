"use client";

import * as React from "react";
import { ChevronDown, ChevronRight, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api } from "@/lib/api";
import { groupPathsState, pathState, toggleGroupPaths, toggleTreePath } from "./storage-cleanup-selection";

type Group = Awaited<ReturnType<typeof api.taskCleanupPreview>>["groups"][number];
type Entries = Awaited<ReturnType<typeof api.workspaceList>>["entries"];
type Category = "traffic" | "llm" | "tool";

const labels: Record<Category, string> = { traffic: "流量录制", llm: "LLM 录制", tool: "任务活动（含工具执行）" };
const lockedLabels: Record<string, string> = { archives: "任务归档", transcripts: "对话转录", other: "其他 ARTEX 文件", database_other: "数据库其他数据" };

function bytes(n: number) {
  if (!n) return "0 B";
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), 4);
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${["B", "KB", "MB", "GB", "TB"][i]}`;
}

function FileTree({ path, protectedPaths, selected, entriesByPath, rememberEntries, onToggle, depth = 0 }: { path: string; protectedPaths: string[]; selected: ReadonlySet<string>; entriesByPath: Record<string, Entries>; rememberEntries: (path: string, entries: Entries) => void; onToggle: (path: string, protectedPaths: string[]) => void; depth?: number }) {
  const [open, setOpen] = React.useState(false);
  const [error, setError] = React.useState("");
  const entries = entriesByPath[path];
  const locked = protectedPaths.some((item) => path === item || path.startsWith(`${item}/`));
  const mixed = !locked && protectedPaths.some((item) => item.startsWith(`${path}/`));
  const toggle = async () => {
    if (open) { setOpen(false); return; }
    setOpen(true);
    if (entries) return;
    try { rememberEntries(path, (await api.workspaceList(path)).entries); }
    catch (e) { setError((e as Error).message); }
  };
  return (
    <div>
      <div className="flex items-center gap-2 rounded px-1 py-1 hover:bg-muted" style={{ paddingLeft: depth * 14 + 4 }}>
        <Checkbox aria-label={`选择 ${path}`} checked={locked ? false : pathState(path, selected)} disabled={locked} onCheckedChange={() => onToggle(path, protectedPaths)} />
        <button type="button" onClick={() => void toggle()} className="flex min-w-0 flex-1 items-center gap-1 text-left" aria-label={`${open ? "收起" : "展开"} ${path}`} aria-expanded={open}>
          {open ? <ChevronDown className="size-3.5 shrink-0" /> : <ChevronRight className="size-3.5 shrink-0" />}
          <span className="truncate">{path.split("/").at(-1)}</span>
        </button>
        <span className="shrink-0 text-xs text-muted-foreground">{locked ? "受保护" : mixed ? "部分受保护" : "可清理"}</span>
      </div>
      {open && (error ? <p className="pl-5 text-xs text-destructive">{error}</p> : entries === undefined ? <p className="pl-5 text-xs text-muted-foreground">读取中…</p> : entries.length === 0 ? <p className="pl-5 text-xs text-muted-foreground">空目录</p> : entries.map((entry) => entry.dir ? (
        <FileTree key={entry.path} path={entry.path} protectedPaths={protectedPaths} selected={selected} entriesByPath={entriesByPath} rememberEntries={rememberEntries} onToggle={onToggle} depth={depth + 1} />
      ) : (
        <div key={entry.path} className="flex items-center gap-2 py-1 text-xs" style={{ paddingLeft: (depth + 1) * 14 + 20 }}>
          <Checkbox aria-label={`选择 ${entry.path}`} checked={protectedPaths.some((item) => entry.path === item || entry.path.startsWith(`${item}/`)) ? false : pathState(entry.path, selected)} disabled={protectedPaths.some((item) => entry.path === item || entry.path.startsWith(`${item}/`))} onCheckedChange={() => onToggle(entry.path, protectedPaths)} />
          <span className="min-w-0 flex-1 truncate" title={entry.path}>{entry.name}</span>
          <span className="shrink-0 text-muted-foreground">{protectedPaths.some((item) => entry.path === item || entry.path.startsWith(`${item}/`)) ? "受保护" : bytes(entry.size)}</span>
        </div>
      )))}
    </div>
  );
}

export function StorageCleanupDialog({ open, onOpenChange, onComplete }: { open: boolean; onOpenChange: (open: boolean) => void; onComplete: () => void }) {
  const [groups, setGroups] = React.useState<Group[]>([]);
  const [parts, setParts] = React.useState<{ key: string; bytes: number }[]>([]);
  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [entriesByPath, setEntriesByPath] = React.useState<Record<string, Entries>>({});
  const [scanning, setScanning] = React.useState(false);
  const [working, setWorking] = React.useState(false);
  const [confirm, setConfirm] = React.useState(false);
  const [error, setError] = React.useState("");

  React.useEffect(() => {
    if (!open) return;
    let active = true;
    setScanning(true);
    setConfirm(false);
    setError("");
    setGroups([]);
    setSelected(new Set());
    setEntriesByPath({});
    void Promise.all([api.taskCleanupPreview(), api.storageStatus()]).then(([preview, status]) => {
      if (!active) return;
      setGroups(preview.groups);
      setParts(status.parts);
      setSelected(new Set(preview.groups.filter((group) => group.risk === "safe").map((group) => group.path)));
    }).catch((e) => { if (active) setError((e as Error).message); }).finally(() => { if (active) setScanning(false); });
    return () => { active = false; };
  }, [open]);

  const toggle = (id: string) => setSelected((current) => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  const selectableGroups = groups.filter((group) => group.risk !== "locked" && group.bytes > 0);
  const toggleGroups = (items: Group[]) => setSelected((current) => toggleGroupPaths(items.filter((group) => group.risk !== "locked" && group.bytes > 0).map((group) => group.path), current));
  const toggleFile = (path: string, protectedPaths: string[]) => setSelected((current) => toggleTreePath(path, current, entriesByPath, protectedPaths));
  const rememberEntries = (path: string, entries: Entries) => setEntriesByPath((current) => ({ ...current, [path]: entries }));
  const chosenPaths = [...selected].filter((path) => path.startsWith("tasks/"));
  const reviewGroups = groups.filter((group) => group.risk === "review" && group.bytes > 0);
  const chosenCategories = (Object.keys(labels) as Category[]).filter((category) => selected.has(category));
  const groupForPath = (path: string) => groups.filter((item) => path === item.path || path.startsWith(`${item.path}/`)).sort((a, b) => b.path.length - a.path.length)[0];
  const reviewSelectionCount = chosenPaths.filter((path) => groupForPath(path)?.risk === "review").length;
  const pathEstimate = (path: string) => {
    const group = groupForPath(path);
    if (!group) return null;
    if (path === group.path) return group.bytes;
    const parent = path.slice(0, path.lastIndexOf("/"));
    const entry = entriesByPath[parent]?.find((item) => item.path === path);
    return entry && !entry.dir ? entry.size : null;
  };
  const pathEstimates = chosenPaths.map(pathEstimate);
  const estimate = pathEstimates.reduce<number>((sum, size) => sum + (size ?? 0), 0) + chosenCategories.reduce((sum, category) => sum + (parts.find((part) => part.key === category)?.bytes ?? 0), 0);
  const estimatePending = pathEstimates.some((size) => size === null);
  const estimateLabel = estimatePending ? `已知 ${bytes(estimate)}，目录待核对` : `预计释放 ${bytes(estimate)}`;
  const chosenCount = chosenPaths.length + chosenCategories.length;
  // shortcut: the API accepts 2,000 paths; add exclusion ranges if large flat folders need one-click partial cleanup.
  const tooManyPaths = chosenPaths.length > 2000;
  const riskOrder: Record<Group["risk"], number> = { safe: 0, review: 1, locked: 2 };
  const workspaceTasks = () => {
    const tasks = new Map<string, Group[]>();
    for (const group of groups) {
      const task = group.path.split("/").slice(0, 2).join("/") || "其他工作区";
      tasks.set(task, [...(tasks.get(task) ?? []), group]);
    }
    return [...tasks.entries()]
      .map(([task, items]) => [task, items.sort((a, b) => riskOrder[a.risk] - riskOrder[b.risk] || a.path.localeCompare(b.path, undefined, { numeric: true }))] as const)
      .sort((a, b) => riskOrder[a[1][0].risk] - riskOrder[b[1][0].risk] || a[0].localeCompare(b[0], undefined, { numeric: true }));
  };
  const renderWorkspace = () => workspaceTasks().map(([task, items]) => (
    <details key={task} className="rounded-md border">
      <summary className="flex cursor-pointer items-center gap-2 p-3 font-medium"><Checkbox aria-label={`选择 ${task} 中的可清理项（含需审查项）`} checked={groupPathsState(items.filter((group) => group.risk !== "locked" && group.bytes > 0).map((group) => group.path), selected)} disabled={!items.some((group) => group.risk !== "locked" && group.bytes > 0)} onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()} onCheckedChange={() => toggleGroups(items)} />{task} <span className="text-xs font-normal text-muted-foreground">· {items.length} 项 · 可选估算 {bytes(items.reduce((sum, item) => sum + item.bytes, 0))}</span></summary>
      <div className="space-y-2 border-t p-3">
        {items.map((group) => <div key={group.path} className="flex items-start gap-3 rounded-md border p-2">
          <Checkbox aria-label={`选择 ${group.path}`} className="mt-0.5" checked={group.risk === "locked" ? false : pathState(group.path, selected)} disabled={group.risk === "locked" || group.bytes === 0} onCheckedChange={() => toggleGroups([group])} />
          <details className="group min-w-0 flex-1">
          <summary className="flex cursor-pointer items-start gap-2 [&::-webkit-details-marker]:hidden">
            <ChevronRight className="mt-1 size-4 shrink-0 group-open:rotate-90" />
            <span className="min-w-0 flex-1"><span className="block truncate font-medium" title={group.path}>{group.path.split("/").slice(2).join("/") || group.path}</span><span className="text-xs text-muted-foreground">{group.risk === "safe" ? "安全清理" : group.risk === "review" ? "需审查" : "不可清理"} · {group.reason}</span></span>
            <span className="shrink-0 text-right tabular-nums">{bytes(group.bytes)}{group.protected_bytes > 0 && <span className="block text-xs text-muted-foreground">保留 {bytes(group.protected_bytes)}</span>}</span>
          </summary>
          {group.dir && !(group.path === task && items.length > 1) && <div className="mt-2 border-t pt-1 text-xs"><FileTree path={group.path} protectedPaths={group.protected_paths ?? []} selected={selected} entriesByPath={entriesByPath} rememberEntries={rememberEntries} onToggle={toggleFile} /></div>}
          </details>
        </div>)}
      </div>
    </details>
  ));

  const clean = async () => {
    setWorking(true);
    try {
      if (chosenPaths.length) await api.taskCleanup(chosenPaths);
      for (const category of chosenCategories) await api.storageCleanup(category);
      toast.success("选中项目已清理，请查看刷新后的空间统计");
      onOpenChange(false);
      onComplete();
    } catch (e) {
      setError(`清理未全部完成：${(e as Error).message}。请重新扫描确认剩余项目。`);
      setConfirm(false);
    } finally { setWorking(false); }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!working) onOpenChange(next); }}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{confirm ? "确认清理空间" : "清理空间"}</DialogTitle>
          <DialogDescription>{confirm ? "删除后无法恢复。执行时会重新核对任务及证据引用；发生变化的项目会停止清理。" : "每次打开都会重新扫描。安全项已勾选，需审查项由你决定，被引用或仍在使用的内容不能勾选。数据库记录清理后的磁盘空闲页会由数据库复用。"}</DialogDescription>
        </DialogHeader>
        {scanning ? <div className="flex flex-col items-center justify-center gap-3 py-12 text-center text-muted-foreground"><Loader2 className="size-6 animate-spin" />正在扫描空间和文件引用…</div> : null}
        {error ? <p role="alert" className="text-sm text-destructive">{error}</p> : null}
        {tooManyPaths ? <p role="alert" className="text-sm text-destructive">一次最多选择 2000 个文件或目录，请选择上级目录或分批清理。</p> : null}
        {!scanning && !confirm && (
          <div className="max-h-[55vh] space-y-2 overflow-y-auto pr-1">
            <details open className="rounded-md border p-3">
              <summary className="flex cursor-pointer items-center gap-2 font-medium"><Checkbox aria-label="选择任务工作区中的可清理项（含需审查项）" checked={groupPathsState(selectableGroups.map((group) => group.path), selected)} disabled={selectableGroups.length === 0} onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()} onCheckedChange={() => toggleGroups(selectableGroups)} />任务工作区 <span className="text-xs font-normal text-muted-foreground">· {groups.filter((group) => group.risk === "safe").length} 项默认勾选 · {reviewGroups.length} 项需审查（预计 {bytes(reviewGroups.reduce((sum, group) => sum + group.bytes, 0))}）</span></summary>
              <div className="mt-3 space-y-2">{renderWorkspace()}</div>
            </details>
            <details className="rounded-md border p-3">
              <summary className="cursor-pointer font-medium">录制与活动 <span className="text-xs font-normal text-amber-600">· 可勾选 · 需审查</span></summary>
              <div className="mt-3 space-y-2">
            {(Object.keys(labels) as Category[]).map((category) => {
              const size = parts.find((part) => part.key === category)?.bytes ?? 0;
              return <div key={category} className="flex items-center gap-3 rounded-md border p-3">
                <Checkbox aria-label={`选择 ${labels[category]}`} checked={selected.has(category)} disabled={size === 0} onCheckedChange={() => toggle(category)} />
                <span className="flex-1">{labels[category]} <span className="text-xs text-amber-600">需审查 · 清空历史</span></span>
                <span className="tabular-nums">{bytes(size)}</span>
              </div>;
            })}
                </div>
            </details>
            <details className="rounded-md border p-3">
              <summary className="cursor-pointer font-medium">保留内容 <span className="text-xs font-normal text-muted-foreground">· 不可勾选 · 受保护</span></summary>
              <div className="mt-3 space-y-2">
            {Object.entries(lockedLabels).map(([key, label]) => {
              const size = parts.find((part) => part.key === key)?.bytes ?? 0;
              return <div key={key} className="rounded-md border p-3">
                <div className="flex items-center gap-3"><Checkbox disabled checked={false} /><span className="flex-1">{label} <span className="text-xs text-muted-foreground">不可清理</span></span><span className="tabular-nums">{bytes(size)}</span></div>
                {(key === "archives" || key === "transcripts") && size > 0 && <div className="mt-2 border-t pt-1 text-xs"><FileTree path={key} protectedPaths={[key]} selected={selected} entriesByPath={entriesByPath} rememberEntries={rememberEntries} onToggle={toggleFile} /></div>}
              </div>;
            })}
                </div>
            </details>
          </div>
        )}
        {!scanning && confirm && <div className="rounded-md border border-destructive/30 bg-destructive/5 p-4">将清理 {chosenPaths.length} 个工作区选择（其中 {reviewSelectionCount} 个需审查）和 {chosenCategories.length} 类历史记录，{estimateLabel}。受保护文件仍会保留，执行前会重新核对。</div>}
        <DialogFooter>
          <Button variant="outline" disabled={working} onClick={() => confirm ? setConfirm(false) : onOpenChange(false)}>{confirm ? "返回选择" : "取消"}</Button>
          <Button variant="destructive" disabled={working || scanning || chosenCount === 0 || tooManyPaths} onClick={() => confirm ? void clean() : setConfirm(true)}>{working ? "清理中…" : confirm ? "确认永久删除" : `清理选中项（${chosenCount} 项，${estimateLabel}）`}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
