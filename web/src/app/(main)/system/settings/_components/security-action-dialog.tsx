"use client";

import * as React from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

export function SecurityActionDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "保存",
  totpEnabled,
  onConfirm,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  confirmLabel?: string;
  totpEnabled: boolean;
  onConfirm: (code: string) => Promise<void>;
  children?: React.ReactNode;
}) {
  const [code, setCode] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await onConfirm(code);
      onOpenChange(false);
      setCode("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!busy) {
          onOpenChange(next);
          if (!next) {
            setCode("");
            setError("");
          }
        }
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <form onSubmit={(event) => void submit(event)} className="space-y-4">
          {children}
          {totpEnabled && (
            <div className="space-y-2">
              <label htmlFor="security-action-code" className="text-sm font-medium">
                TOTP 验证码
              </label>
              <Input
                id="security-action-code"
                aria-label="TOTP 验证码"
                autoComplete="one-time-code"
                inputMode="numeric"
                pattern="[0-9]{6}"
                maxLength={6}
                value={code}
                onChange={(event) => setCode(event.target.value.replace(/\D/g, ""))}
                required
                disabled={busy}
                placeholder="输入 6 位验证码后继续"
              />
            </div>
          )}
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={busy || (totpEnabled && code.length !== 6)}>
              {busy ? "验证中…" : confirmLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
