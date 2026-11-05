"use client";

import * as React from "react";

import Link from "next/link";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";

import { ArrowUpCircleIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { api } from "@/lib/api";
import type { UpdateCheck } from "@/lib/types";

const SKIPPED_VERSION = "artex_skipped_update_version";

export function UpdateBadge({ hideBadge = false }: { hideBadge?: boolean }) {
  const [info, setInfo] = React.useState<UpdateCheck | null>(null);
  const [open, setOpen] = React.useState(false);
  const lastCheck = React.useRef(0);

  React.useEffect(() => {
    let alive = true;
    const check = () => {
      lastCheck.current = Date.now();
      // 每次进入网页回源。服务端的 30 分钟缓存只供其他非强制调用使用。
      api
        .checkUpdate(true)
        .then((result) => {
          if (!alive) return;
          setInfo(result);
          setOpen(
            Boolean(result.has_update && result.latest && localStorage.getItem(SKIPPED_VERSION) !== result.latest),
          );
        })
        .catch(() => {
          if (alive) { setInfo(null); setOpen(false); }
          // 网络暂不可用时不阻塞页面，设置页仍可手动查看错误。
        });
    };
    check();
    const onVisible = () => {
      if (document.visibilityState === "visible" && Date.now() - lastCheck.current > 60_000) check();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      alive = false;
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, []);

  const latest =
    info?.has_update &&
    info.latest &&
    (typeof window === "undefined" || localStorage.getItem(SKIPPED_VERSION) !== info.latest)
      ? info.latest
      : "";

  const skip = () => {
    if (info?.latest) localStorage.setItem(SKIPPED_VERSION, info.latest);
    setOpen(false);
    setInfo(null);
  };

  return (
    <>
      {!hideBadge && latest ? (
        <Link
          href="/system/settings/?category=system"
          onClick={() => window.dispatchEvent(new Event("artex-open-updates"))}
          className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs"
        >
          <ArrowUpCircleIcon className="size-3.5" />
          新版本 {latest}
        </Link>
      ) : null}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="flex h-[min(72dvh,38rem)] max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] flex-col overflow-hidden sm:max-w-md">
          <DialogHeader className="pr-7">
            <DialogTitle className="text-lg">发现新版本 {info?.latest}</DialogTitle>
            <DialogDescription>当前版本 {info?.current}。更新会重启程序，正在运行的任务会被中断。</DialogDescription>
          </DialogHeader>
          <div className="min-h-0 flex-1 overflow-y-auto rounded-lg bg-muted p-4 text-sm leading-relaxed [overflow-wrap:anywhere] [&_a]:text-primary [&_a]:underline [&_h1]:text-base [&_h1]:font-semibold [&_h2]:font-semibold [&_h3]:font-semibold [&_ol]:list-decimal [&_ol]:pl-5 [&_p]:my-2 [&_pre]:overflow-x-auto [&_pre]:rounded [&_pre]:bg-background [&_pre]:p-2 [&_ul]:list-disc [&_ul]:pl-5">
            <ReactMarkdown
              remarkPlugins={[remarkGfm]}
              skipHtml
              urlTransform={defaultUrlTransform}
              components={{
                a: ({ node, ...props }) => <a {...props} target="_blank" rel="noopener noreferrer" />,
                img: () => null,
              }}
            >
              {info?.notes || "此版本暂无更新说明。"}
            </ReactMarkdown>
          </div>
          <DialogFooter className="gap-2">
            <Button variant="outline" onClick={skip}>
              跳过这个版本
            </Button>
            <Button asChild>
              <Link href="/system/settings/?category=system" onClick={() => { setOpen(false); window.dispatchEvent(new Event("artex-open-updates")); }}>查看更新</Link>
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
