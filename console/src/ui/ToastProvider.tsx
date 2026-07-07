// Transient-notification stack replacing the blocking native window.alert():
//   const toast = useToast();
//   toast("clone に失敗: …");                 // defaults to a STICKY error toast
//   toast("保存しました", { kind: "success" }); // auto-dismisses
// Port of the old ToastProvider (errors sticky + role=alert; others polite).
import { createContext, useCallback, useContext, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "./Icon.tsx";

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

export function ToastProvider({ children }: { children: ReactNode }) {
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
      // Errors are sticky so a failure can't be missed; others auto-dismiss.
      const duration = opts?.duration ?? (kind === "error" ? 0 : 4000);
      if (duration > 0) setTimeout(() => remove(id), duration);
    },
    [remove],
  );

  return (
    <ToastCtx.Provider value={toast}>
      {children}
      {items.length > 0 && (
        <div className="ui-toasts">
          {items.map((t) => (
            <div
              key={t.id}
              className={"ui-toast ui-toast-" + t.kind}
              role={t.kind === "error" ? "alert" : "status"}
              aria-live={t.kind === "error" ? "assertive" : "polite"}
            >
              <Icon name={ICONS[t.kind]} />
              <span className="ui-toast-msg">{t.message}</span>
              <button type="button" className="ui-toast-x" title="閉じる" onClick={() => remove(t.id)}>
                <Icon name="close" />
              </button>
            </div>
          ))}
        </div>
      )}
    </ToastCtx.Provider>
  );
}
