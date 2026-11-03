"use client";

import { useEffect, useState } from "react";
import { REGEXP_ONLY_DIGITS } from "input-otp";
import { Button } from "@/components/ui/button";
import { InputOTP, InputOTPGroup, InputOTPSlot } from "@/components/ui/input-otp";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function TOTPPage() {
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (auth.getToken()) { window.location.replace("/function/tasks"); return; }
    if (!sessionStorage.getItem("artex_pending_totp")) window.location.replace("/login");
  }, []);
  async function verify(event: React.FormEvent) {
    event.preventDefault();
    const pending = sessionStorage.getItem("artex_pending_totp");
    if (!pending) { window.location.replace("/login"); return; }
    setBusy(true); setError("");
    try {
      const { token } = await api.finishTOTP(pending, code);
      sessionStorage.removeItem("artex_pending_totp");
      auth.setToken(token);
      window.location.replace("/function/tasks");
    } catch (e) { setError((e as Error).message); setCode(""); }
    finally { setBusy(false); }
  }
  return <main className="flex min-h-dvh items-center justify-center bg-background p-6">
    <form onSubmit={(e) => void verify(e)} className="w-full max-w-sm space-y-6 rounded-xl border bg-card p-8 shadow-sm">
      <div className="space-y-2 text-center"><h1 className="text-2xl font-semibold">TOTP 两步验证</h1><p className="text-sm text-muted-foreground">请输入验证器中的 6 位动态码</p></div>
      <div className="flex justify-center"><InputOTP maxLength={6} pattern={REGEXP_ONLY_DIGITS} value={code} onChange={setCode} autoComplete="one-time-code" autoFocus><InputOTPGroup>{Array.from({ length: 6 }, (_, i) => <InputOTPSlot key={i} index={i} />)}</InputOTPGroup></InputOTP></div>
      {error && <p role="alert" className="text-center text-sm text-destructive">{error}</p>}
      <Button className="w-full" type="submit" disabled={busy || code.length !== 6}>{busy ? "验证中…" : "验证并继续"}</Button>
      <Button className="w-full" type="button" variant="ghost" onClick={() => { sessionStorage.removeItem("artex_pending_totp"); window.location.replace("/login"); }}>返回登录</Button>
    </form>
  </main>;
}
