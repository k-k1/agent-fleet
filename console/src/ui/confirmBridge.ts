// Imperative access to the app's <ConfirmProvider> for code that lives OUTSIDE the
// React tree — the vanilla xterm manager in terminal/term.ts can't call the useConfirm()
// hook. ConfirmProvider registers its Promise-based confirm here on mount; askConfirm()
// routes to it. Before the provider has mounted we fall back to the native confirm() so
// a call is never silently lost (only string title/body survive that fallback).
import type { ConfirmOptions } from "./ConfirmProvider.tsx";
import { t } from "../lib/i18n/index.ts";

type ConfirmFn = (opts: ConfirmOptions) => Promise<boolean>;

let impl: ConfirmFn | null = null;

export function registerConfirm(fn: ConfirmFn | null): void {
  impl = fn;
}

export function askConfirm(opts: ConfirmOptions): Promise<boolean> {
  if (impl) return impl(opts);
  const parts = [opts.title, opts.body].filter((x): x is string => typeof x === "string");
  return Promise.resolve(window.confirm(parts.join("\n\n") || t("ui.confirm_continue")));
}
