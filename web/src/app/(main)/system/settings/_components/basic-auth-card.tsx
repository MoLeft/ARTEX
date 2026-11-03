"use client";

import { useCallback, useEffect, useState } from "react";

import { ShieldCheckIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";

import { useTOTPStatus } from "./account-security-card";
import { SecurityActionDialog } from "./security-action-dialog";

type Action = "enable" | "edit" | "disable";

export function BasicAuthCard() {
  const [enabled, setEnabled] = useState(false);
  const [username, setUsername] = useState("");
  const [passwordSet, setPasswordSet] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [action, setAction] = useState<Action | null>(null);
  const [draftUsername, setDraftUsername] = useState("");
  const [draftPassword, setDraftPassword] = useState("");
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

  function openAction(next: Action) {
    setDraftUsername(username);
    setDraftPassword("");
    setAction(next);
  }

  async function save(code: string) {
    if (!action) return;
    const settings = await api.setBasicAuthSettings({
      enabled: action !== "disable",
      username: action === "disable" ? username : draftUsername.trim(),
      password: action === "disable" ? "" : draftPassword,
      code,
    });
    setEnabled(settings.enabled);
    setUsername(settings.username);
    setPasswordSet(settings.password_set);
    setDraftPassword("");
    toast.success(action === "disable" ? "HTTP Basic Auth 已关闭" : "HTTP Basic Auth 已保存，当前浏览器保持访问");
  }

  const editCredentials = action === "enable" || action === "edit";

  return (
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader className="flex flex-wrap items-center justify-between gap-4">
        <div className="space-y-1.5">
          <CardTitle className="flex items-center gap-2">
            <ShieldCheckIcon className="size-4" />
            HTTP Basic Auth
          </CardTitle>
          <CardDescription>
            开启后，访客需先通过浏览器原生账号密码验证，再进入 ARTEX 登录页。保存后立即生效。
          </CardDescription>
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-2">
          {enabled && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={!loaded || action !== null || totpEnabled === null}
              onClick={() => openAction("edit")}
            >
              修改账号密码
            </Button>
          )}
          <Switch
            aria-label="启用访问验证"
            checked={enabled}
            disabled={!loaded || action !== null || totpEnabled === null}
            onCheckedChange={(next) => openAction(next ? "enable" : "disable")}
          />
        </div>
      </CardHeader>
      {error && (
        <CardContent>
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
          {!loaded && (
            <Button type="button" variant="outline" onClick={() => void load()}>
              重新加载
            </Button>
          )}
        </CardContent>
      )}
      <SecurityActionDialog
        open={action !== null}
        onOpenChange={(open) => {
          if (!open) {
            setAction(null);
            setDraftPassword("");
          }
        }}
        title={
          action === "disable"
            ? "关闭 HTTP Basic Auth"
            : action === "edit"
              ? "修改 HTTP Basic Auth 账号密码"
              : "开启 HTTP Basic Auth"
        }
        description={
          action === "disable"
            ? "关闭后，访客不再需要通过浏览器访问验证。"
            : "填写账号密码并保存后立即生效，当前浏览器保持访问。"
        }
        confirmLabel={action === "disable" ? "确认关闭" : action === "edit" ? "保存修改" : "保存并开启"}
        totpEnabled={totpEnabled === true}
        onConfirm={save}
      >
        {editCredentials && (
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="basic-auth-username">验证用户名</FieldLabel>
              <Input
                id="basic-auth-username"
                autoComplete="off"
                value={draftUsername}
                required
                onChange={(event) => setDraftUsername(event.target.value)}
                placeholder="独立于 ARTEX 登录账号"
              />
              <FieldDescription>不能包含冒号，最长 128 字节。</FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="basic-auth-password">验证密码</FieldLabel>
              <Input
                id="basic-auth-password"
                type="password"
                autoComplete="new-password"
                value={draftPassword}
                required={!passwordSet}
                onChange={(event) => setDraftPassword(event.target.value)}
                placeholder={passwordSet ? "留空沿用原密码" : "至少 8 位，最多 72 字节"}
              />
              <FieldDescription>与 ARTEX 登录密码独立。公网访问请使用 HTTPS。</FieldDescription>
            </Field>
          </FieldGroup>
        )}
      </SecurityActionDialog>
    </Card>
  );
}
