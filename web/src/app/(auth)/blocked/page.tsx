"use client";

import { useEffect, useState } from "react";
import { auth } from "@/lib/auth";

export default function BlockedPage() {
  const [seconds, setSeconds] = useState(10);
  useEffect(() => {
    const timer = window.setInterval(() => setSeconds((value) => Math.max(0, value - 1)), 1000);
    const exit = window.setTimeout(() => { auth.clearToken(); sessionStorage.removeItem("artex_pending_totp"); window.location.replace("/login"); }, 10000);
    return () => { window.clearInterval(timer); window.clearTimeout(exit); };
  }, []);
  return <main className="flex min-h-dvh items-center justify-center bg-background p-6"><div role="alert" className="w-full max-w-md space-y-4 rounded-xl border bg-card p-8 text-center shadow-sm"><h1 className="text-xl font-semibold text-destructive">访问已被拦截</h1><p className="text-muted-foreground">当前 IP 不在白名单内，无法操作系统。</p><p className="tabular-nums">{seconds} 秒后自动退出登录</p></div></main>;
}
