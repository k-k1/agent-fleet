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

export function toast(message: ReactNode, opts?: ToastOptions): void {
  sink?.(message, opts);
}

// dismissToast withdraws the toast shown with { key }, without running its onClose: the
// owner withdraws it because what it announced is settled, not because the member closed it.
export function dismissToast(key: string): void {
  dismissSink?.(key);
}
