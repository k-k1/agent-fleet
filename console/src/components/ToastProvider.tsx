import { createContext, useCallback, useContext, useRef, useState } from "react";
import type { ReactNode } from "react";
import Icon from "./Icon.jsx";

// A lightweight transient-notification stack, so scattered handlers can replace the
// blocking native window.alert() with a non-blocking toast:
//   const toast = useToast();
//   toast("clone に失敗: …");            // defaults to an error toast
//   toast("保存しました", { kind: "success" });
// The provider mounts a single fixed stack (bottom-center) and auto-dismisses each
// toast after a kind-dependent duration; pass duration: 0 to keep it until closed.
export type ToastKind = "error" | "warn" | "info" | "success";

export interface ToastOptions {
  kind?: ToastKind;
  duration?: number; // ms; 0 = sticky (manual close only)
}

interface ToastItem {
  id: number;
  message: ReactNode;
  kind: ToastKind;
}

type ToastFn = (message: ReactNode, opts?: ToastOptions) => void;

const ToastCtx = createContext<ToastFn | null>(null);

export function useToast(): ToastFn {
  const fn = useContext(ToastCtx);
  if (!fn) throw new Error("useToast must be used within <ToastProvider>");
  return fn;
}

const ICONS: Record<ToastKind, string> = {
  error: "error",
  warn: "warning",
  info: "info",
  success: "pass",
};

export default function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const seq = useRef(0);

  const remove = useCallback((id: number) => {
    setItems((xs) => xs.filter((x) => x.id !== id));
  }, []);

  const toast = useCallback<ToastFn>(
    (message, opts) => {
      const kind = opts?.kind ?? "error";
      const id = ++seq.current;
      setItems((xs) => [...xs, { id, message, kind }]);
      // Errors linger a little longer since they carry something to read/act on.
      const duration = opts?.duration ?? (kind === "error" ? 6000 : 4000);
      if (duration > 0) setTimeout(() => remove(id), duration);
    },
    [remove],
  );

  return (
    <ToastCtx.Provider value={toast}>
      {children}
      {items.length > 0 && (
        <div className="toast-stack" role="status" aria-live="polite">
          {items.map((t) => (
            <div key={t.id} className={"toast toast-" + t.kind}>
              <Icon name={ICONS[t.kind]} className="toast-ic" />
              <span className="toast-msg">{t.message}</span>
              <button type="button" className="toast-x" title="閉じる" onClick={() => remove(t.id)}>
                <Icon name="close" />
              </button>
            </div>
          ))}
        </div>
      )}
    </ToastCtx.Provider>
  );
}
