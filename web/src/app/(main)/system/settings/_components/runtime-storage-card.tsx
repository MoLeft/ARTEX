"use client";

import * as React from "react";
import { Cell, Pie, PieChart, ResponsiveContainer, Tooltip } from "recharts";

import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { StorageCleanupDialog } from "./storage-cleanup-dialog";
import { api } from "@/lib/api";
import { MOCK } from "@/lib/mock/enabled";
import type { SystemMetrics } from "@/lib/types";

type StorageStatus = Awaited<ReturnType<typeof api.storageStatus>>;
type Retention = Awaited<ReturnType<typeof api.recordRetention>>;

function bytes(n: number) {
  if (!n) return "0 B";
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), 4);
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${["B", "KB", "MB", "GB", "TB"][i]}`;
}

function storageBytes(n: number) { return `${(n / 1e9).toFixed(1)} GB`; }

const storageColors = [
  "#2563eb", "#14b8a6", "#f59e0b", "#8b5cf6",
  "#ef4444", "#64748b", "#0ea5e9", "#84cc16",
];

function UsageRing({ percent, label, color, available = true }: { percent: number; label: string; color: string; available?: boolean }) {
  const value = Math.max(0, Math.min(100, Number.isFinite(percent) ? percent : 0));
  return <div className="relative size-20 shrink-0" role="img" aria-label={available ? `${label} ${value.toFixed(0)}%` : `${label}不可用`}>
    <svg className="size-full -rotate-90" viewBox="0 0 80 80" aria-hidden="true">
      <circle cx="40" cy="40" r="32" fill="none" stroke="var(--muted)" strokeWidth="8" />
      {available && <circle cx="40" cy="40" r="32" fill="none" stroke={color} strokeWidth="8" strokeLinecap="round" strokeDasharray={`${value * 2.0106} 201.06`} />}
    </svg>
    <span className="absolute inset-0 flex items-center justify-center text-sm font-semibold tabular-nums">{available ? `${value.toFixed(0)}%` : "—"}</span>
  </div>;
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
  const [cleanerOpen, setCleanerOpen] = React.useState(false);

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

  React.useEffect(() => {
    const timer = window.setInterval(() => { void api.systemMetrics().then(setMetrics).catch(() => {}); }, 5000);
    return () => window.clearInterval(timer);
  }, []);

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

  const optimizeMemory = async () => {
    setBusy(true);
    try {
      await api.storageCleanup("memory");
      toast.success("已请求回收 ARTEX 空闲堆内存");
      await refresh();
    } catch (e) {
      toast.error(`清理失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  const chartParts = status?.parts.filter((part) => part.bytes > 0) ?? [];
  const hostStorage = metrics?.data_disk_source === "host";
  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>运行和空间</CardTitle>
          <CardDescription>
            分类占用显示 ARTEX 文件及 PostgreSQL 数据库估算；储存空间优先显示宿主机磁盘容量。饼图各部分之和不等于整块硬盘已用空间。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          {error ? (
            <p role="alert" className="text-sm text-destructive">
              读取失败：{error}
            </p>
          ) : null}
          <div className="grid gap-3 text-sm" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 20rem), 1fr))" }}>
            <div className="flex items-center justify-between gap-2 rounded-md border p-3">
              <div className="min-w-0 flex-1"><div>操作系统 / 处理器</div><strong className="break-words">{status ? `${status.os} / ${status.arch}` : "读取中…"}</strong><p className="text-xs text-muted-foreground">{status?.os_scope === "host" ? "宿主机系统" : "容器系统"} · 容器处理器使用率</p></div>
              <UsageRing percent={metrics?.cpu.percent ?? 0} label="处理器使用率" color="#2563eb" />
            </div>
            <div className="flex items-center justify-between gap-2 rounded-md border p-3">
              <div className="min-w-0 flex-1"><div>运行内存</div><strong className="break-words">{metrics?.memory.total_bytes ? `${bytes(metrics.memory.used_bytes)} / ${bytes(metrics.memory.total_bytes)}` : "不可用"}</strong>
                <p className="text-xs text-muted-foreground">ARTEX 进程堆：{bytes(status?.process_heap_bytes ?? 0)}</p>
                <Button variant="outline" size="sm" className="mt-2" disabled={busy} onClick={() => void optimizeMemory()}>优化内存</Button>
              </div>
              <UsageRing percent={metrics?.memory.percent ?? 0} label="运行内存使用率" color="#14b8a6" />
            </div>
            <div className="flex items-center justify-between gap-2 rounded-md border p-3">
              <div className="min-w-0 flex-1"><div>储存空间</div><strong className="break-words">{hostStorage && metrics?.data_disk.total_bytes ? `${storageBytes(metrics.data_disk.used_bytes)} / ${storageBytes(metrics.data_disk.total_bytes)}` : "宿主机容量待接入"}</strong>
                <p className="text-xs text-muted-foreground">{hostStorage && metrics ? `可用：${storageBytes(Math.max(0, metrics.data_disk.total_bytes - metrics.data_disk.used_bytes))} · 宿主机` : "容器挂载容量不代表物理硬盘"}</p>
                <Button variant="outline" size="sm" className="mt-2" onClick={() => setCleanerOpen(true)}>清理空间</Button>
              </div>
              <UsageRing percent={metrics?.data_disk.percent ?? 0} label="储存空间占用率" color="#f59e0b" available={hostStorage} />
            </div>
          </div>
          <div>
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-sm font-medium">分类占用</h3>
              <Button size="sm" variant="outline" onClick={() => void refresh()}>
                刷新统计
              </Button>
            </div>
            {chartParts.length > 0 && (
              <div className="h-64 w-full" role="img" aria-label="ARTEX 各类数据空间占比饼状图">
                <ResponsiveContainer width="100%" height="100%" minWidth={0} minHeight={0}>
                  <PieChart>
                    <Pie data={chartParts} dataKey="bytes" nameKey="name" cx="50%" cy="50%" innerRadius={55} outerRadius={105} paddingAngle={2}>
                      {chartParts.map((part) => (
                        <Cell key={part.key} fill={storageColors[(status?.parts.findIndex((item) => item.key === part.key) ?? 0) % storageColors.length]} />
                      ))}
                    </Pie>
                    <Tooltip formatter={(value) => bytes(Number(value))} />
                  </PieChart>
                </ResponsiveContainer>
              </div>
            )}
            <div className="grid gap-2 sm:grid-cols-2">
              {status?.parts.map((part, index) => (
                <div key={part.key} className="flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm">
                    <span className="flex min-w-0 items-center gap-2"><span className="size-2.5 shrink-0 rounded-full" style={{ backgroundColor: storageColors[index % storageColors.length] }} />{part.name}</span>
                    <span className="tabular-nums">{bytes(part.bytes)}</span>
                </div>
              ))}
            </div>
            <p className="mt-2 text-xs text-muted-foreground">
              清理空间会先扫描并标出安全、需审查和受保护的内容；对话转录及任务归档保持只读。PostgreSQL
              局部删除后的空闲页由数据库复用，磁盘文件未必立即缩小。
            </p>
          </div>
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
      <StorageCleanupDialog open={cleanerOpen} onOpenChange={setCleanerOpen} onComplete={() => void refresh()} />
    </>
  );
}
