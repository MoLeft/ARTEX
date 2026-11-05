"use client";

import * as React from "react";

import { ArrowUpIcon, DownloadIcon, FileIcon, FolderIcon, Loader2Icon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";
import type { TaskWorkspaceTree, WorkspaceEntry, WorkspaceFile, WorkspaceListing } from "@/lib/types";

import { fileIndex, resolveFileReference, taskBrowserPath } from "./file-references";

const CodePreview = React.lazy(() => import("./code-preview"));

const mediaLimit = 20 * 1024 * 1024;

export function useFindingWorkspace(taskId?: string) {
  const [trees, setTrees] = React.useState<Record<string, TaskWorkspaceTree>>({});
  const requestedTasks = React.useRef(new Set<string>());
  const currentTask = React.useRef(taskId);
  currentTask.current = taskId;
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState("");
  const [selected, setSelected] = React.useState<string | null>(null);
  const [selectedEntry, setSelectedEntry] = React.useState<WorkspaceEntry | null>(null);
  const [directory, setDirectory] = React.useState(taskId ? `tasks/${taskId}` : "tasks");
  const [listing, setListing] = React.useState<WorkspaceListing | null>(null);
  const [browserLoading, setBrowserLoading] = React.useState(false);
  const [browserError, setBrowserError] = React.useState("");

  const loadTask = React.useCallback((id: string) => {
    if (!taskBrowserPath(`tasks/${id}`) || requestedTasks.current.has(id)) return;
    requestedTasks.current.add(id);
    if (id === currentTask.current) setLoading(true);
    api
      .taskWorkspaceTree(id)
      .then((result) => {
        setTrees((current) => ({ ...current, [id]: result }));
      })
      .catch((cause) => {
        if (id === currentTask.current) setError((cause as Error).message);
      })
      .finally(() => {
        if (id === currentTask.current) setLoading(false);
      });
  }, []);

  React.useEffect(() => {
    setSelected(null);
    setSelectedEntry(null);
    setError("");
    setDirectory(taskId ? `tasks/${taskId}` : "tasks");
    if (taskId) loadTask(taskId);
  }, [taskId, loadTask]);

  React.useEffect(() => {
    let active = true;
    setListing(null);
    setBrowserError("");
    setBrowserLoading(true);
    api
      .workspaceList(directory)
      .then((result) => {
        if (active)
          setListing({
            ...result,
            entries: result.entries.filter(
              (entry) =>
                entry.path.startsWith(`${directory}/`) &&
                !entry.path.slice(directory.length + 1).includes("/") &&
                taskBrowserPath(entry.path) &&
                (directory !== "tasks" || entry.dir),
            ),
          });
      })
      .catch((cause) => {
        if (active) setBrowserError((cause as Error).message);
      })
      .finally(() => {
        if (active) setBrowserLoading(false);
      });
    return () => {
      active = false;
    };
  }, [directory]);

  const paths = React.useMemo(() => {
    const primary = taskId ? trees[taskId] : null;
    const index = fileIndex(primary?.entries ?? [], taskId ?? "", primary?.root ?? "");
    for (const [id, tree] of Object.entries(trees)) {
      if (id === taskId) continue;
      for (const [key, path] of fileIndex(tree.entries, id, tree.root, false)) index.set(key, path);
    }
    return index;
  }, [taskId, trees]);
  const resolve = React.useCallback(
    (value: string) => (taskId ? resolveFileReference(value, paths) : null),
    [paths, taskId],
  );
  const ensure = React.useCallback(
    (text: string) => {
      for (const match of text.matchAll(/tasks\/([1-9]\d*)\//g)) loadTask(match[1]);
    },
    [loadTask],
  );
  const browse = React.useCallback(
    (path: string) => {
      if (!taskBrowserPath(path)) return;
      setDirectory(path);
      const sibling = path.split("/")[1];
      if (sibling) loadTask(sibling);
    },
    [loadTask],
  );
  const open = React.useCallback(
    (path: string) => {
      if (!taskBrowserPath(path) || path.split("/").length < 3) return;
      const listed = listing?.entries.find((entry) => entry.path === path && !entry.dir);
      if (!listed && paths.get(path) !== path) return;
      const file =
        listed ??
        Object.values(trees)
          .flatMap((tree) => tree.entries)
          .find((entry) => entry.path === path && !entry.dir);
      if (!file) return;
      setSelectedEntry(file);
      setSelected(path);
      browse(path.slice(0, path.lastIndexOf("/")));
    },
    [browse, listing, paths, trees],
  );
  const entry = React.useCallback(
    (path: string | null) => {
      if (!path) return undefined;
      return (
        (selectedEntry?.path === path ? selectedEntry : undefined) ??
        listing?.entries.find((item) => item.path === path && !item.dir) ??
        Object.values(trees)
          .flatMap((tree) => tree.entries)
          .find((item) => item.path === path && !item.dir)
      );
    },
    [listing, selectedEntry, trees],
  );
  return {
    trees,
    loading,
    error,
    selected,
    setSelected,
    resolve,
    ensure,
    open,
    entry,
    directory,
    entries: listing?.entries ?? [],
    browse,
    browserLoading,
    browserError,
  };
}

function FileBrowser({ workspace }: { workspace: ReturnType<typeof useFindingWorkspace> }) {
  return (
    <>
      <div className="mb-2 flex min-w-0 items-center gap-1 border-b pb-2">
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label="返回上一级"
          title="返回上一级"
          disabled={workspace.directory === "tasks"}
          onClick={() => workspace.browse(workspace.directory.slice(0, workspace.directory.lastIndexOf("/")))}
        >
          <ArrowUpIcon />
        </Button>
        <span className="min-w-0 truncate font-mono text-xs text-muted-foreground" title={workspace.directory}>
          {workspace.directory}
        </span>
      </div>
      <div className="max-h-[48vh] overflow-auto font-mono">
        {workspace.browserLoading ? <p className="py-2 text-xs text-muted-foreground">读取目录中…</p> : null}
        {workspace.browserError ? (
          <p className="py-2 text-xs text-destructive">读取失败：{workspace.browserError}</p>
        ) : null}
        {!workspace.browserLoading && !workspace.browserError && workspace.entries.length === 0 ? (
          <p className="py-2 text-xs text-muted-foreground">此目录为空</p>
        ) : null}
        {workspace.entries.map((entry: WorkspaceEntry) => (
          <button
            key={entry.path}
            type="button"
            title={entry.path}
            className={`flex w-full min-w-0 items-center gap-2 rounded px-1 py-1 text-left text-xs hover:bg-muted ${workspace.selected === entry.path ? "bg-primary/10 text-primary" : ""}`}
            onClick={() => (entry.dir ? workspace.browse(entry.path) : workspace.open(entry.path))}
          >
            {entry.dir ? <FolderIcon className="size-3.5 shrink-0" /> : <FileIcon className="size-3.5 shrink-0" />}
            <span className="truncate">{entry.name}</span>
          </button>
        ))}
      </div>
    </>
  );
}

export function FindingWorkspaceCard({
  taskId,
  workspace,
}: {
  taskId?: string;
  workspace: ReturnType<typeof useFindingWorkspace>;
}) {
  if (!taskId) return null;
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="flex items-center gap-2 text-sm">
          <FolderIcon className="size-4" /> 工作区文件
        </CardTitle>
      </CardHeader>
      <CardContent className="min-w-0">
        <FileBrowser workspace={workspace} />
        {workspace.loading ? <p className="mt-2 text-xs text-muted-foreground">正在索引文件链接…</p> : null}
        {workspace.error ? <p className="mt-2 text-xs text-destructive">文件链接索引失败：{workspace.error}</p> : null}
        {workspace.trees[workspace.directory.split("/")[1]]?.truncated ? (
          <p className="mt-2 text-xs text-amber-600">自动文件链接仅索引前 20,000 项，目录仍可继续浏览。</p>
        ) : null}
      </CardContent>
    </Card>
  );
}

export function FindingWorkspaceSheet({
  taskId,
  workspace,
}: {
  taskId?: string;
  workspace: ReturnType<typeof useFindingWorkspace>;
}) {
  const path = workspace.selected;
  const entry = workspace.entry(path);
  const [file, setFile] = React.useState<WorkspaceFile | null>(null);
  const [mediaURL, setMediaURL] = React.useState("");
  const [error, setError] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const ext = path?.split(".").pop()?.toLowerCase() ?? "";
  const media = ["png", "jpg", "jpeg", "gif", "webp", "pdf", "mp3", "wav", "ogg", "mp4", "webm"].includes(ext);

  React.useEffect(() => {
    let active = true;
    let objectURL = "";
    setFile(null);
    setMediaURL("");
    setError("");
    if (!path || !entry) return;
    if (media && entry.size > mediaLimit) return;
    setLoading(true);
    if (media) {
      const token = auth.getToken();
      fetch(`/api/workspace/download?path=${encodeURIComponent(path)}`, {
        credentials: "same-origin",
        headers: token ? { Authorization: `Bearer ${token}` } : undefined,
      })
        .then((response) => {
          if (!response.ok) throw new Error(`读取失败：${response.status}`);
          return response.blob();
        })
        .then((blob) => {
          if (active) {
            objectURL = URL.createObjectURL(blob);
            setMediaURL(objectURL);
          }
        })
        .catch((cause) => {
          if (active) setError((cause as Error).message);
        })
        .finally(() => {
          if (active) setLoading(false);
        });
    } else {
      api
        .workspaceRead(path)
        .then((result) => {
          if (active) setFile(result);
        })
        .catch((cause) => {
          if (active) setError((cause as Error).message);
        })
        .finally(() => {
          if (active) setLoading(false);
        });
    }
    return () => {
      active = false;
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [entry, media, path]);

  return (
    <Sheet
      open={!!path}
      onOpenChange={(open) => {
        if (!open) workspace.setSelected(null);
      }}
    >
      <SheetContent className="w-full! max-w-none! gap-0 p-0 sm:w-[min(94vw,1100px)]! sm:max-w-none!">
        <SheetHeader className="border-b pr-12">
          <SheetTitle className="truncate font-mono text-sm" title={path ?? ""}>
            {path ?? "文件预览"}
          </SheetTitle>
          <p className="text-xs text-muted-foreground">
            只读预览 · {entry ? `${(entry.size / 1024).toFixed(1)} KB` : ""}
          </p>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col sm:flex-row">
          <aside className="max-h-48 w-full shrink-0 overflow-auto border-b p-2 sm:max-h-none sm:w-64 sm:border-r sm:border-b-0">
            {taskId ? <FileBrowser workspace={workspace} /> : null}
          </aside>
          <div className="min-h-0 min-w-0 flex-1 overflow-auto p-4">
            {loading ? (
              <div className="flex items-center gap-2 text-muted-foreground">
                <Loader2Icon className="size-4 animate-spin" /> 读取中…
              </div>
            ) : null}
            {error ? <p className="text-destructive">{error}</p> : null}
            {!loading && !error && mediaURL && ["png", "jpg", "jpeg", "gif", "webp"].includes(ext) ? (
              // biome-ignore lint/performance/noImgElement: Blob URLs cannot use Next image optimization.
              <img src={mediaURL} alt={entry?.name ?? "文件"} className="max-h-full max-w-full object-contain" />
            ) : null}
            {!loading && !error && mediaURL && ext === "pdf" ? (
              <iframe src={mediaURL} title={entry?.name ?? "PDF"} className="h-full min-h-[70vh] w-full border" />
            ) : null}
            {!loading && !error && mediaURL && ["mp3", "wav", "ogg"].includes(ext) ? (
              // biome-ignore lint/a11y/useMediaCaption: User workspace audio may not have a caption file.
              <audio src={mediaURL} controls className="w-full" />
            ) : null}
            {!loading && !error && mediaURL && ["mp4", "webm"].includes(ext) ? (
              // biome-ignore lint/a11y/useMediaCaption: User workspace video may not have a caption file.
              <video src={mediaURL} controls className="max-h-full max-w-full" />
            ) : null}
            {!loading && !error && file?.content !== undefined ? (
              <React.Suspense fallback={<p className="text-muted-foreground text-sm">准备代码预览…</p>}>
                <CodePreview path={file.path} content={file.content} />
              </React.Suspense>
            ) : null}
            {!loading && !error && ((media && (entry?.size ?? 0) > mediaLimit) || file?.binary || file?.too_large) ? (
              <p className="text-sm text-muted-foreground">此文件无法在线预览，可下载查看。</p>
            ) : null}
          </div>
        </div>
        {path ? (
          <div className="flex justify-end border-t p-3">
            <Button variant="outline" size="sm" onClick={() => void api.workspaceDownload(path)}>
              <DownloadIcon /> 下载文件
            </Button>
          </div>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}
