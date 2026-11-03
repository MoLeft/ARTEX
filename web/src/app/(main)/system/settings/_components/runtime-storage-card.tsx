"use client";

import * as React from "react";

import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import { MOCK } from "@/lib/mock/enabled";
import type { SystemMetrics } from "@/lib/types";

type StorageStatus = Awaited<ReturnType<typeof api.storageStatus>>;
type Retention = Awaited<ReturnType<typeof api.recordRetention>>;
type Cleanup = "memory" | "traffic" | "llm" | "tool";

function bytes(n: number) {
  if (!n) return "0 B";
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), 4);
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${["B", "KB", "MB", "GB", "TB"][i]}`;
}

const retentionFields = [
  ["traffic_days", "流量录制"],
  ["llm_days", "LLM 录制"],
  ["tool_days", "工具执行"],
] as const;

export function RuntimeStorageCard() {
  const [status, setStatus] = React.useState<StorageStatus | null>(null);
  const [metrics, setMetrics] = React.useState<SystemMetrics | null>(null);
  const [retention, setRetention] = React.useState<Retention | null>(null);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [cleanup, setCleanup] = React.useState<Cleanup | null>(null);

  const refresh = React.useCallback(async () => {
    try {
      const [nextStatus, nextMetrics] = await Promise.all([api.storageStatus(), api.systemMetrics()]);
      setStatus(nextStatus);
      setMetrics(nextMetrics);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  React.useEffect(() => {
    void refresh();
    api
      .recordRetention()
      .then(setRetention)
      .catch((e) => setError((e as Error).message));
  }, [refresh]);

  const save = async () => {
    if (!retention) return;
    if (
      retentionFields.some(([key]) => !Number.isInteger(retention[key]) || retention[key] < 0 || retention[key] > 3650)
    ) {
      toast.error("保留天数须为 0–3650 的整数");
      return;
    }
    setBusy(true);
    try {
      setRetention(await api.saveRecordRetention(retention));
      toast.success("保留期限已保存，后台将执行到期清理");
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  const runCleanup = async () => {
    if (!cleanup) return;
    setBusy(true);
    try {
      const result = await api.storageCleanup(cleanup);
      toast.success(cleanup === "memory" ? "已请求回收 ARTEX 空闲堆内存" : `已删除 ${result.deleted} 条录制记录`);
      setCleanup(null);
      await refresh();
    } catch (e) {
      toast.error(`清理失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  const max = Math.max(1, ...(status?.parts.map((part) => part.bytes) ?? []));
  const selected = status?.parts.find((part) => part.key === cleanup);
  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>运行和空间</CardTitle>
          <CardDescription>
            系统指标显示 ARTEX 所在容器和数据卷；分类占用为 ARTEX 文件及 PostgreSQL 数据库估算，不等于整块硬盘已用空间。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          {error ? (
            <p role="alert" className="text-sm text-destructive">
              读取失败：{error}
            </p>
          ) : null}
          <div className="grid gap-3 text-sm sm:grid-cols-3">
            <div className="rounded-md border p-3">
              操作系统
              <br />
              <strong>{status ? `${status.os} / ${status.arch}` : "读取中…"}</strong>
              <p className="text-xs text-muted-foreground">容器视角</p>
            </div>
            <div className="rounded-md border p-3">
              运行内存
              <br />
              <strong>
                {metrics?.memory.total_bytes
                  ? `${bytes(metrics.memory.used_bytes)} / ${bytes(metrics.memory.total_bytes)}`
                  : "不可用"}
              </strong>
              <p className="text-xs text-muted-foreground">ARTEX 进程堆：{bytes(status?.process_heap_bytes ?? 0)}</p>
            </div>
            <div className="rounded-md border p-3">
              数据卷空间
              <br />
              <strong>
                {metrics?.data_disk.total_bytes
                  ? `${bytes(metrics.data_disk.used_bytes)} / ${bytes(metrics.data_disk.total_bytes)}`
                  : "不可用"}
              </strong>
              <p className="text-xs text-muted-foreground">
                可用：{bytes(Math.max(0, (metrics?.data_disk.total_bytes ?? 0) - (metrics?.data_disk.used_bytes ?? 0)))}
              </p>
            </div>
          </div>
          <div>
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-sm font-medium">分类占用</h3>
              <Button size="sm" variant="outline" onClick={() => void refresh()}>
                刷新统计
              </Button>
            </div>
            <div className="space-y-3">
              {status?.parts.map((part) => (
                <div key={part.key}>
                  <div className="mb-1 flex items-center justify-between gap-2 text-xs">
                    <span>{part.name}</span>
                    <span className="tabular-nums">
                      {bytes(part.bytes)}{" "}
                      {part.cleanable && (
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 text-destructive"
                          disabled={busy || part.bytes === 0}
                          onClick={() => setCleanup(part.key as Cleanup)}
                        >
                          清理
                        </Button>
                      )}
                    </span>
                  </div>
                  <div className="h-3 overflow-hidden rounded bg-muted">
                    <div
                      className="h-full rounded bg-primary"
                      style={{ width: `${part.bytes ? Math.max(1, (part.bytes / max) * 100) : 0}%` }}
                    />
                  </div>
                </div>
              ))}
            </div>
            <p className="mt-2 text-xs text-muted-foreground">
              “任务活动”包含其他活动，清理只删除工具调用及对应结果。PostgreSQL
              局部删除后的空闲页由数据库复用，磁盘文件未必立即缩小。
            </p>
          </div>
          <Button variant="outline" size="sm" disabled={busy} onClick={() => setCleanup("memory")}>
            优化 ARTEX 内存
          </Button>
          <p className="text-xs text-muted-foreground">
            内存优化只请求 Go 运行时归还空闲堆页；不会清理系统缓存或中断任务。
          </p>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>自动保留期限</CardTitle>
          <CardDescription>
            流量先临时录制，确认漏洞时复制到独立证据库；普通流量按下面的期限自动清理。0 表示永久保留。
            每日检查到期数据，保存后也会触发一次后台检查。已绑定漏洞的证据副本不会被清理。
            {MOCK ? " 演示模式只保存页面设置，不执行定时清理。" : ""}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {retentionFields.map(([key, label]) => (
            <label key={key} htmlFor={`retention-${key}`} className="flex items-center justify-between gap-3 text-sm">
              <span>{label}</span>
              <span className="flex items-center gap-2">
                <Input
                  id={`retention-${key}`}
                  type="number"
                  min={0}
                  max={3650}
                  step={1}
                  className="w-28"
                  value={retention?.[key] ?? 0}
                  onChange={(e) => setRetention((prev) => prev && { ...prev, [key]: Number(e.target.value) })}
                />
                天
              </span>
            </label>
          ))}
          <p className="text-xs text-muted-foreground">
            工具执行到期清理会同时移除任务活动历史中的对应调用与结果；任务、归档、漏洞证据及 Token
            计量账本不会自动删除。
          </p>
          <Button disabled={!retention || busy} onClick={() => void save()}>
            保存保留期限
          </Button>
        </CardContent>
      </Card>
      <AlertDialog
        open={cleanup !== null}
        onOpenChange={(open) => {
          if (!open && !busy) setCleanup(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {cleanup === "memory" ? "回收 ARTEX 空闲堆内存？" : `清空${selected?.name ?? "录制记录"}？`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {cleanup === "memory"
                ? "只触发 ARTEX 进程的 Go 垃圾回收；操作系统已使用内存可能不会明显变化。"
                : "将永久删除该分类的全部录制历史，忽略列表筛选。流量原文随流量清理，工具执行及其结果会从任务活动历史移除；已绑定漏洞的独立证据保留。"}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>取消</AlertDialogCancel>
            <AlertDialogAction
              disabled={busy}
              onClick={(e) => {
                e.preventDefault();
                void runCleanup();
              }}
            >
              {busy ? "处理中…" : "确认执行"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
