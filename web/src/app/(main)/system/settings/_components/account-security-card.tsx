"use client";

import * as React from "react";

import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { createPasskey } from "@/lib/passkey";

import { SecurityActionDialog } from "./security-action-dialog";

function deviceFromUA(ua: string): string {
  const platform = /iPhone OS ([\d_]+)/.exec(ua) ?? /CPU OS ([\d_]+)/.exec(ua);
  if (platform) return `iOS ${platform[1].replaceAll("_", ".")}`;
  const mac = /Mac OS X ([\d_.]+)/.exec(ua);
  if (mac) return `Mac ${mac[1].replaceAll("_", ".")}`;
  const windows = /Windows NT ([\d.]+)/.exec(ua);
  if (windows) return `Windows ${windows[1] === "10.0" ? "10 / 11" : windows[1]}`;
  const android = /Android ([\d.]+)/.exec(ua);
  if (android) return `Android ${android[1]}`;
  if (/Linux/.test(ua)) return "Linux";
  return "未知设备";
}

export function useTOTPStatus() {
  const [enabled, setEnabled] = React.useState<boolean | null>(null);
  React.useEffect(() => {
    let active = true;
    const load = () => {
      void api
        .totpSettings()
        .then((value) => {
          if (active) setEnabled(value.enabled);
        })
        .catch((e) => toast.error((e as Error).message));
    };
    load();
    window.addEventListener("artex-totp-changed", load);
    return () => {
      active = false;
      window.removeEventListener("artex-totp-changed", load);
    };
  }, []);
  return enabled;
}

export function SessionsCard() {
  const [data, setData] = React.useState<Awaited<ReturnType<typeof api.sessions>> | null>(null);
  const [target, setTarget] = React.useState<{ id: string; device: string; ip: string } | null>(null);
  const totpEnabled = useTOTPStatus();
  const load = React.useCallback(async () => {
    try {
      setData(await api.sessions());
    } catch (e) {
      toast.error((e as Error).message);
    }
  }, []);
  React.useEffect(() => {
    void load();
  }, [load]);
  async function revoke(code: string) {
    if (!target) return;
    await api.revokeSession(target.id, code);
    toast.success("该会话已退出");
    await load();
  }
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <div>
          <CardTitle>登录设备与会话</CardTitle>
          <CardDescription>
            当前有效会话：{data?.count ?? "读取中…"}。设备和系统版本根据浏览器 UA 识别。
          </CardDescription>
        </div>
        <Button variant="outline" size="sm" onClick={() => void load()}>
          刷新
        </Button>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="max-h-72 space-y-2 overflow-y-auto">
          {data?.sessions.map((item) => (
            <div key={item.id} className="flex items-center gap-3 rounded-md border p-3 text-sm">
              <div className="min-w-0 flex-1">
                <div className="font-medium">
                  {deviceFromUA(item.user_agent)} {item.current && <span className="text-primary">· 当前设备</span>}
                </div>
                <div className="text-muted-foreground">
                  IP：{item.ip} · {item.factor === "passkey" ? "通行密钥" : "密码"}登录 · 最近活动{" "}
                  {new Date(item.last_seen_at).toLocaleString()}
                </div>
                <div className="truncate text-xs text-muted-foreground" title={item.user_agent}>
                  {item.user_agent}
                </div>
              </div>
              {!item.current && <Button variant="destructive" size="sm" disabled={totpEnabled === null} onClick={() => setTarget({ id: item.id, device: deviceFromUA(item.user_agent), ip: item.ip })}>退出</Button>}
            </div>
          ))}
        </div>
      </CardContent>
      <SecurityActionDialog
        open={target !== null}
        onOpenChange={(open) => { if (!open) setTarget(null); }}
        title="退出此设备会话"
        description={target ? `${target.device} · ${target.ip}。该设备会立即失去登录状态。` : undefined}
        confirmLabel="确认退出"
        totpEnabled={totpEnabled === true}
        onConfirm={revoke}
      />
    </Card>
  );
}

type WhitelistAction = { kind: "new" } | { kind: "remove"; cidr: string } | { kind: "toggle"; enabled: boolean };

export function IPWhitelistCard() {
  const [data, setData] = React.useState<Awaited<ReturnType<typeof api.ipWhitelist>> | null>(null);
  const [action, setAction] = React.useState<WhitelistAction | null>(null);
  const [input, setInput] = React.useState("");
  const totpEnabled = useTOTPStatus();
  React.useEffect(() => {
    void api
      .ipWhitelist()
      .then(setData)
      .catch((e) => toast.error((e as Error).message));
  }, []);
  async function save(code: string) {
    if (!data || !action) return;
    const cidrs =
      action.kind === "new"
        ? [...data.cidrs, input.trim()]
        : action.kind === "remove"
          ? data.cidrs.filter((value) => value !== action.cidr)
          : action.enabled && !data.cidrs.includes(data.current_ip)
            ? [...data.cidrs, data.current_ip]
            : data.cidrs;
    if (action.kind === "new" && (!input.trim() || data.cidrs.includes(input.trim())))
      throw new Error("请输入尚未添加的 IP 地址或网段");
    const result = await api.saveIPWhitelist(action.kind === "toggle" ? action.enabled : data.enabled, cidrs, code);
    setData(result);
    setInput("");
    toast.success("IP 白名单已保存");
  }
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <div className="space-y-1.5">
          <CardTitle>IP 白名单</CardTitle>
          <CardDescription>
            支持多个 IPv4、IPv6 地址或 CIDR 网段。当前访问 IP：{data?.current_ip || "读取中…"}。
          </CardDescription>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {data?.enabled && (
            <Button
              size="sm"
              variant="outline"
              disabled={totpEnabled === null}
              onClick={() => {
                setInput("");
                setAction({ kind: "new" });
              }}
            >
              新建
            </Button>
          )}
          <Switch
            aria-label="启用 IP 白名单"
            checked={data?.enabled ?? false}
            disabled={!data || totpEnabled === null}
            onCheckedChange={(enabled) => setAction({ kind: "toggle", enabled })}
          />
        </div>
      </CardHeader>
      {data?.enabled && (
        <CardContent className="space-y-2">
          {data.cidrs.map((cidr) => (
            <div key={cidr} className="flex items-center justify-between rounded border px-3 py-2 text-sm">
              <code className="break-all">{cidr}</code>
              <Button variant="ghost" size="sm" onClick={() => setAction({ kind: "remove", cidr })}>
                移除
              </Button>
            </div>
          ))}
        </CardContent>
      )}
      <SecurityActionDialog
        open={action !== null}
        onOpenChange={(open) => {
          if (!open) setAction(null);
        }}
        title={
          action?.kind === "new"
            ? "新建 IP 白名单记录"
            : action?.kind === "remove"
              ? "移除 IP 白名单记录"
              : action?.enabled
                ? "开启 IP 白名单"
                : "关闭 IP 白名单"
        }
        description={
          action?.kind === "toggle" && action.enabled ? "首次开启时会自动加入当前访问 IP，避免锁定自己。" : undefined
        }
        confirmLabel={action?.kind === "remove" ? "确认移除" : "保存"}
        totpEnabled={totpEnabled === true}
        onConfirm={save}
      >
        {action?.kind === "new" && (
          <Input
            aria-label="IP 地址或网段"
            placeholder="例如 192.0.2.0/24 或 2001:db8::/32"
            value={input}
            onChange={(event) => setInput(event.target.value)}
            required
          />
        )}
      </SecurityActionDialog>
    </Card>
  );
}

type PasskeyAction = { kind: "new" } | { kind: "rename" | "remove"; id: string; remark: string };

export function PasskeysCard() {
  const [data, setData] = React.useState<Awaited<ReturnType<typeof api.passkeys>> | null>(null);
  const [action, setAction] = React.useState<PasskeyAction | null>(null);
  const [remark, setRemark] = React.useState("");
  const [password, setPassword] = React.useState("");
  const totpEnabled = useTOTPStatus();
  const load = React.useCallback(async () => {
    try {
      setData(await api.passkeys());
    } catch (e) {
      toast.error((e as Error).message);
    }
  }, []);
  React.useEffect(() => {
    void load();
  }, [load]);
  async function save(code: string) {
    if (!action) return;
    if (action.kind === "new") {
      if (!remark.trim() || (totpEnabled === false && !password)) throw new Error("请填写备注和验证信息");
      const { challenge_id, options } = await api.passkeyRegisterBegin(remark.trim(), password, code);
      const credential = await createPasskey(options);
      await api.passkeyRegisterFinish(challenge_id, credential);
      toast.success("通行密钥已添加");
    } else if (action.kind === "rename") {
      if (!remark.trim()) throw new Error("备注不能为空");
      await api.updatePasskey(action.id, remark.trim(), code);
      toast.success("备注已更新");
    } else {
      await api.deletePasskey(action.id, code);
      toast.success("通行密钥已删除");
    }
    setRemark("");
    setPassword("");
    await load();
  }
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <div className="space-y-1.5">
          <CardTitle>通行密钥</CardTitle>
          <CardDescription>
            可与账号密码同时使用。每把密钥可单独命名；注册和登录须使用 HTTPS 或本机地址。
            {data?.origin && `当前绑定站点：${data.origin}`}
          </CardDescription>
        </div>
        <Button
          size="sm"
          variant="outline"
          disabled={!data || totpEnabled === null}
          onClick={() => {
            setRemark("");
            setPassword("");
            setAction({ kind: "new" });
          }}
        >
          新建
        </Button>
      </CardHeader>
      <CardContent className="space-y-2">
        {data?.passkeys.map((key) => (
          <div key={key.id} className="flex flex-wrap items-center gap-2 rounded border p-3 text-sm">
            <div className="min-w-0 flex-1">
              <div className="font-medium">{key.remark}</div>
              <div className="text-xs text-muted-foreground">
                添加：{new Date(key.created_at).toLocaleString()}
                {key.last_used_at && ` · 最近使用：${new Date(key.last_used_at).toLocaleString()}`}
              </div>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setRemark(key.remark);
                setAction({ kind: "rename", id: key.id, remark: key.remark });
              }}
            >
              改备注
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={() => setAction({ kind: "remove", id: key.id, remark: key.remark })}
            >
              删除
            </Button>
          </div>
        ))}
      </CardContent>
      <SecurityActionDialog
        open={action !== null}
        onOpenChange={(open) => {
          if (!open) setAction(null);
        }}
        title={
          action?.kind === "new" ? "新建通行密钥" : action?.kind === "rename" ? "修改通行密钥备注" : "删除通行密钥"
        }
        description={action?.kind === "remove" ? `确定删除“${action.remark}”？` : "填写信息并完成身份验证后保存。"}
        confirmLabel={action?.kind === "remove" ? "确认删除" : "验证并保存"}
        totpEnabled={totpEnabled === true}
        onConfirm={save}
      >
        {action?.kind !== "remove" && (
          <Input
            aria-label="通行密钥备注"
            placeholder="备注，例如 Mac mini"
            value={remark}
            onChange={(event) => setRemark(event.target.value)}
            required
          />
        )}
        {action?.kind === "new" && totpEnabled === false && (
          <Input
            aria-label="当前密码"
            type="password"
            autoComplete="current-password"
            placeholder="当前密码"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            required
          />
        )}
      </SecurityActionDialog>
    </Card>
  );
}

export function LoginFailureNotifyCard() {
  const [enabled, setEnabled] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [pending, setPending] = React.useState<boolean | null>(null);
  const totpEnabled = useTOTPStatus();
  React.useEffect(() => {
    void api
      .loginFailureNotify()
      .then((v) => setEnabled(v.enabled))
      .catch((e) => toast.error((e as Error).message));
  }, []);
  async function save(value: boolean, code: string) {
    setBusy(true);
    try {
      await api.saveLoginFailureNotify(value, code);
      setEnabled(value);
      toast.success(value ? "登录失败通知已开启" : "登录失败通知已关闭");
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <div className="space-y-1.5">
          <CardTitle>登录失败通知</CardTitle>
          <CardDescription>
            密码错误、TOTP 错误和通行密钥验证失败会经现有通知渠道发送安全事件；同一 IP 的同类事件一分钟内合并。
          </CardDescription>
        </div>
        <Switch aria-label="启用登录失败通知" checked={enabled} disabled={busy || totpEnabled === null} onCheckedChange={setPending} />
      </CardHeader>
      <SecurityActionDialog open={pending !== null} onOpenChange={(open) => { if (!open) setPending(null); }} title={pending ? "开启登录失败通知" : "关闭登录失败通知"} totpEnabled={totpEnabled === true} onConfirm={(code) => save(pending ?? enabled, code)} />
    </Card>
  );
}
