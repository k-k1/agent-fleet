import type { ReactNode } from "react";
import type { ToastOptions } from "./ToastProvider.tsx";

// Imperative toast for non-React callers (keyboard commands, engine callbacks). React's
// <ToastProvider> registers its sink on mount and clears it on unmount; calls made before
// that (or after unmount) are dropped — there is no live UI to show them. Inside components
// prefer useToast(); this bridge exists only for code that runs outside the React tree.
type ToastFn = (message: ReactNode, opts?: ToastOptions) => void;
type DismissFn = (key: string) => void;

let sink: ToastFn | null = null;
let dismissSink: DismissFn | null = null;

export function registerToastSink(fn: ToastFn | null, dismiss: DismissFn | null = null): void {
  sink = fn;
  dismissSink = dismiss;
}

// toast returns whether a provider took the call: one made before <ToastProvider> registered
// (a child's effects run before its parent's) is dropped, and a caller that remembers what it
// has shown must not remember that one.
export function toast(message: ReactNode, opts?: ToastOptions): boolean {
  if (!sink) return false;
  sink(message, opts);
  return true;
}

// dismissToast withdraws the toast shown with { key }, without running its onClose: the
// owner withdraws it because what it announced is settled, not because the member closed it.
export function dismissToast(key: string): void {
  dismissSink?.(key);
}
