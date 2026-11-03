"use client";

import { useCallback, useEffect, useState } from "react";

import { ShieldCheckIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { useTOTPStatus } from "./account-security-card";
import { SecurityActionDialog } from "./security-action-dialog";

export function BasicAuthCard() {
  const [enabled, setEnabled] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [passwordSet, setPasswordSet] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const totpEnabled = useTOTPStatus();

  const load = useCallback(async () => {
    setError("");
    try {
      const settings = await api.basicAuthSettings();
      setEnabled(settings.enabled);
      setUsername(settings.username);
      setPasswordSet(settings.password_set);
      setLoaded(true);
    } catch (e) {
      setError(`读取失败：${(e as Error).message}`);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async (code: string) => {
    setSaving(true);
    setError("");
    try {
      const settings = await api.setBasicAuthSettings({ enabled, username: username.trim(), password, code });
      setEnabled(settings.enabled);
      setUsername(settings.username);
      setPasswordSet(settings.password_set);
      setPassword("");
      toast.success(settings.enabled ? "HTTP Basic Auth 已开启，当前浏览器保持访问" : "HTTP Basic Auth 已关闭");
    } catch (e) {
      setError(`保存失败：${(e as Error).message}`);
      throw e;
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <div className="space-y-1.5">
        <CardTitle className="flex items-center gap-2">
          <ShieldCheckIcon className="size-4" />
          HTTP Basic Auth
        </CardTitle>
        <CardDescription>
          开启后，访客需先通过浏览器原生账号密码验证，再进入 ARTEX 登录页。默认关闭，保存后立即生效。
        </CardDescription>
        </div>
        <Switch aria-label="启用访问验证" checked={enabled} onCheckedChange={setEnabled} disabled={!loaded || saving} />
      </CardHeader>
      <form onSubmit={(event) => { event.preventDefault(); setConfirmOpen(true); }}>
        <CardContent>
          <FieldGroup>
            {enabled && <Field>
              <FieldLabel htmlFor="basic-auth-username">验证用户名</FieldLabel>
              <Input
                id="basic-auth-username"
                autoComplete="off"
                value={username}
                required={enabled}
                disabled={!loaded || saving}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="独立于 ARTEX 登录账号"
              />
              <FieldDescription>不能包含冒号，最长 128 字节。</FieldDescription>
            </Field>}
            {enabled && <Field>
              <FieldLabel htmlFor="basic-auth-password">验证密码</FieldLabel>
              <Input
                id="basic-auth-password"
                type="password"
                autoComplete="new-password"
                value={password}
                required={enabled && !passwordSet}
                disabled={!loaded || saving}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={passwordSet ? "已设置，留空保持原密码" : "至少 8 位，最多 72 字节"}
              />
              <FieldDescription>与 ARTEX 登录密码独立。公网访问请使用 HTTPS。</FieldDescription>
            </Field>}
            {enabled && <FieldDescription>
              保存不会中断当前浏览器访问。可用无痕窗口检查验证弹窗；浏览器可能缓存凭据。关闭后保留账号密码，便于再次开启。
            </FieldDescription>}
            {error ? (
              <p role="alert" className="text-destructive text-sm">
                {error}
              </p>
            ) : null}
          </FieldGroup>
        </CardContent>
        <CardFooter className="mt-4 justify-end gap-2">
          <Button type="submit" disabled={!loaded || saving || totpEnabled === null}>
            {saving ? "保存中…" : "保存访问验证"}
          </Button>
          {!loaded && error ? (
            <Button type="button" variant="outline" onClick={load}>
              重新加载
            </Button>
          ) : null}
        </CardFooter>
      </form>
      <SecurityActionDialog open={confirmOpen} onOpenChange={setConfirmOpen} title="保存 HTTP Basic Auth" description="确认修改访问验证设置。" totpEnabled={totpEnabled === true} onConfirm={save} />
    </Card>
  );
}
