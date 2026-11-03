"use client";

import * as React from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { createPasskey } from "@/lib/passkey";

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

export function SessionsCard() {
  const [data, setData] = React.useState<Awaited<ReturnType<typeof api.sessions>> | null>(null);
  const [totpEnabled, setTotpEnabled] = React.useState(false);
  const [code, setCode] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const load = React.useCallback(async () => {
    try { const [sessions, totp] = await Promise.all([api.sessions(), api.totpSettings()]); setData(sessions); setTotpEnabled(totp.enabled); }
    catch (e) { toast.error((e as Error).message); }
  }, []);
  React.useEffect(() => { void load(); }, [load]);
  async function revoke() {
    setBusy(true);
    try { const result = await api.revokeOtherSessions(code); toast.success(`已退出 ${result.revoked} 个其他会话`); setCode(""); await load(); }
    catch (e) { toast.error((e as Error).message); }
    finally { setBusy(false); }
  }
  return <Card><CardHeader><CardTitle>登录设备与会话</CardTitle><CardDescription>当前有效会话：{data?.count ?? "读取中…"}。设备和系统版本根据浏览器 UA 识别。</CardDescription></CardHeader><CardContent className="space-y-4">
    <div className="max-h-72 space-y-2 overflow-y-auto">{data?.sessions.map((item) => <div key={item.id} className="rounded-md border p-3 text-sm"><div className="font-medium">{deviceFromUA(item.user_agent)} {item.current && <span className="text-primary">· 当前设备</span>}</div><div className="text-muted-foreground">IP：{item.ip} · {item.factor === "passkey" ? "通行密钥" : "密码"}登录 · 最近活动 {new Date(item.last_seen_at).toLocaleString()}</div><div className="truncate text-xs text-muted-foreground" title={item.user_agent}>{item.user_agent}</div></div>)}</div>
    <div className="flex flex-wrap items-center gap-2">{totpEnabled && <Input aria-label="强制退出其他设备的 TOTP 验证码" placeholder="输入 6 位 TOTP 验证码" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} className="max-w-56" />}<Button variant="destructive" disabled={busy || (data?.count ?? 0) <= 1 || (totpEnabled && code.length !== 6)} onClick={() => void revoke()}>强制退出其他设备</Button><Button variant="outline" onClick={() => void load()}>刷新</Button></div>
  </CardContent></Card>;
}

export function IPWhitelistCard() {
  const [enabled, setEnabled] = React.useState(false);
  const [cidrs, setCidrs] = React.useState<string[]>([]);
  const [input, setInput] = React.useState("");
  const [currentIP, setCurrentIP] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  React.useEffect(() => { void api.ipWhitelist().then((v) => { setEnabled(v.enabled); setCidrs(v.cidrs); setCurrentIP(v.current_ip); }).catch((e) => toast.error((e as Error).message)); }, []);
  async function save() {
    setBusy(true);
    try { const result = await api.saveIPWhitelist(enabled, cidrs); setEnabled(result.enabled); setCidrs(result.cidrs); toast.success("IP 白名单已保存"); }
    catch (e) { toast.error((e as Error).message); }
    finally { setBusy(false); }
  }
  function add() { const value = input.trim(); if (value && !cidrs.includes(value)) setCidrs([...cidrs, value]); setInput(""); }
  return <Card><CardHeader><CardTitle>IP 白名单</CardTitle><CardDescription>支持多个 IPv4、IPv6 地址或 CIDR 网段。当前访问 IP：{currentIP || "未知"}。开启后，白名单外的请求会被全局拦截。</CardDescription></CardHeader><CardContent className="space-y-4"><div className="flex items-center gap-3"><Switch aria-label="启用 IP 白名单" checked={enabled} onCheckedChange={setEnabled} /><span>启用 IP 白名单</span></div>
    {enabled && <div className="space-y-3"><div className="flex gap-2"><Input aria-label="新增 IP 地址或网段" placeholder="例如 192.0.2.0/24 或 2001:db8::/32" value={input} onChange={(e) => setInput(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }} /><Button type="button" variant="outline" onClick={add}>添加</Button></div><Button type="button" variant="outline" size="sm" onClick={() => { if (currentIP && !cidrs.includes(currentIP)) setCidrs([...cidrs, currentIP]); }}>添加当前 IP</Button><div className="max-h-48 space-y-1 overflow-y-auto">{cidrs.map((cidr) => <div key={cidr} className="flex items-center justify-between rounded border px-3 py-1 text-sm"><code>{cidr}</code><Button type="button" variant="ghost" size="sm" onClick={() => setCidrs(cidrs.filter((v) => v !== cidr))}>移除</Button></div>)}</div></div>}
    <Button disabled={busy} onClick={() => void save()}>保存白名单</Button>
  </CardContent></Card>;
}

export function PasskeysCard() {
  const [data, setData] = React.useState<Awaited<ReturnType<typeof api.passkeys>> | null>(null);
  const [totpEnabled, setTotpEnabled] = React.useState(false);
  const [remark, setRemark] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [code, setCode] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const load = React.useCallback(async () => { try { const [keys, totp] = await Promise.all([api.passkeys(), api.totpSettings()]); setData(keys); setTotpEnabled(totp.enabled); } catch (e) { toast.error((e as Error).message); } }, []);
  React.useEffect(() => { void load(); }, [load]);
  async function register() {
    setBusy(true);
    try { const { challenge_id, options } = await api.passkeyRegisterBegin(remark, password, code); const credential = await createPasskey(options); await api.passkeyRegisterFinish(challenge_id, credential); toast.success("通行密钥已添加"); setRemark(""); setPassword(""); setCode(""); await load(); }
    catch (e) { toast.error((e as Error).message); }
    finally { setBusy(false); }
  }
  async function rename(id: string, old: string) { const next = window.prompt("通行密钥备注", old)?.trim(); if (!next || next === old) return; try { await api.updatePasskey(id, next); await load(); } catch (e) { toast.error((e as Error).message); } }
  async function remove(id: string) { if (!window.confirm("删除这把通行密钥？")) return; try { await api.deletePasskey(id); await load(); } catch (e) { toast.error((e as Error).message); } }
  return <Card><CardHeader><CardTitle>通行密钥</CardTitle><CardDescription>可与账号密码同时使用。每把密钥可单独命名；注册和登录须使用 HTTPS 或本机地址。{data?.origin && `当前绑定站点：${data.origin}`}</CardDescription></CardHeader><CardContent className="space-y-4"><div className="space-y-2">{data?.passkeys.map((key) => <div key={key.id} className="flex flex-wrap items-center gap-2 rounded border p-3 text-sm"><div className="min-w-0 flex-1"><div className="font-medium">{key.remark}</div><div className="text-xs text-muted-foreground">添加：{new Date(key.created_at).toLocaleString()}{key.last_used_at && ` · 最近使用：${new Date(key.last_used_at).toLocaleString()}`}</div></div><Button variant="outline" size="sm" onClick={() => void rename(key.id, key.remark)}>改备注</Button><Button variant="destructive" size="sm" onClick={() => void remove(key.id)}>删除</Button></div>)}</div>
    <div className="grid gap-2 sm:grid-cols-3"><Input aria-label="通行密钥备注" placeholder="备注，例如 Mac mini" value={remark} onChange={(e) => setRemark(e.target.value)} />{totpEnabled ? <Input aria-label="注册通行密钥的 TOTP 验证码" placeholder="6 位 TOTP 验证码" value={code} onChange={(e) => setCode(e.target.value)} maxLength={6} inputMode="numeric" /> : <Input aria-label="注册通行密钥的当前密码" placeholder="当前密码" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />}<Button disabled={busy || !remark.trim() || (totpEnabled ? code.length !== 6 : !password)} onClick={() => void register()}>添加通行密钥</Button></div>
  </CardContent></Card>;
}

export function LoginFailureNotifyCard() {
  const [enabled, setEnabled] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  React.useEffect(() => { void api.loginFailureNotify().then((v) => setEnabled(v.enabled)).catch((e) => toast.error((e as Error).message)); }, []);
  async function save(value: boolean) { setBusy(true); try { await api.saveLoginFailureNotify(value); setEnabled(value); toast.success(value ? "登录失败通知已开启" : "登录失败通知已关闭"); } catch (e) { toast.error((e as Error).message); } finally { setBusy(false); } }
  return <Card><CardHeader><CardTitle>登录失败通知</CardTitle><CardDescription>密码错误、TOTP 错误和通行密钥验证失败会经现有通知渠道发送安全事件；同一 IP 的同类事件一分钟内合并。</CardDescription></CardHeader><CardContent><div className="flex items-center gap-3"><Switch aria-label="启用登录失败通知" checked={enabled} disabled={busy} onCheckedChange={(v) => void save(v)} /><span>{enabled ? "已开启" : "已关闭"}</span></div></CardContent></Card>;
}
