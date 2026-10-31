"use client";

import { useCallback, useEffect, useState } from "react";

import { ShieldCheckIcon } from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";

export function TOTPCard() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [enrollment, setEnrollment] = useState<{ secret: string; uri: string } | null>(null);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setError("");
    try {
      setEnabled((await api.totpSettings()).enabled);
    } catch (e) {
      setError(`读取失败：${(e as Error).message}`);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function beginSetup() {
    setBusy(true);
    setError("");
    try {
      setEnrollment(await api.setupTOTP());
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function save(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (enrollment) {
        await api.enableTOTP(password, enrollment.secret, code);
        setEnabled(true);
        setEnrollment(null);
        toast.success("TOTP 已启用");
      } else {
        await api.disableTOTP(password, code);
        setEnabled(false);
        toast.success("TOTP 已关闭");
      }
      setPassword("");
      setCode("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const hasForm = Boolean(enabled || enrollment);
  const actionLabel = enabled ? "关闭 TOTP" : "验证并启用";
  let statusText = "正在读取 TOTP 状态…";
  if (enabled === true) statusText = "已启用。以后登录需输入验证器显示的六位动态码。";
  if (enabled === false) statusText = "未启用。登录页仍显示验证码输入框，可留空登录。";

  return (
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <ShieldCheckIcon className="size-4" />
          TOTP 两步验证
        </CardTitle>
        <CardDescription>{statusText}</CardDescription>
      </CardHeader>
      <CardContent>
        {enabled === false && !enrollment ? (
          <Button type="button" onClick={beginSetup} disabled={busy}>
            {busy ? "生成中…" : "配置验证器"}
          </Button>
        ) : null}
        {enrollment ? (
          <div className="mb-4 space-y-3">
            <p className="text-sm">用验证器扫描二维码，或手动输入下方密钥。验证通过后才会启用。</p>
            <div className="w-fit rounded-lg bg-white p-3">
              <QRCodeSVG value={enrollment.uri} size={160} aria-label="TOTP 配置二维码" />
            </div>
            <p className="break-all rounded-md bg-muted p-2 font-mono text-sm select-all">{enrollment.secret}</p>
            <FieldDescription>密钥只在本次配置时显示，请妥善保存。</FieldDescription>
          </div>
        ) : null}
        {hasForm ? (
          <form id="totp-form" onSubmit={save}>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="totp-password">当前 ARTEX 密码</FieldLabel>
                <Input
                  id="totp-password"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  disabled={busy}
                  required
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="totp-code">验证器动态码</FieldLabel>
                <Input
                  id="totp-code"
                  type="text"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  pattern="[0-9]{6}"
                  maxLength={6}
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  disabled={busy}
                  required
                  placeholder="6 位数字"
                />
              </Field>
              <FieldDescription>如果验证器丢失，需要服务器管理员重置 TOTP。</FieldDescription>
              {error ? (
                <p role="alert" className="text-sm text-destructive">
                  {error}
                </p>
              ) : null}
            </FieldGroup>
          </form>
        ) : null}
        {!hasForm && error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : null}
        {enabled === null && error ? (
          <Button type="button" variant="outline" onClick={load}>
            重新加载
          </Button>
        ) : null}
      </CardContent>
      {hasForm ? (
        <CardFooter className="mt-4 gap-2">
          <Button
            type="submit"
            form="totp-form"
            disabled={busy || code.length !== 6 || !password}
            variant={enabled ? "destructive" : "default"}
          >
            {busy ? "保存中…" : actionLabel}
          </Button>
          {enrollment ? (
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={() => {
                setEnrollment(null);
                setPassword("");
                setCode("");
                setError("");
              }}
            >
              取消
            </Button>
          ) : null}
        </CardFooter>
      ) : null}
    </Card>
  );
}
