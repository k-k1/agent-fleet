// Transient-notification stack replacing the blocking native window.alert():
//   const toast = useToast();
//   toast("clone failed: …");                // error: auto-dismiss + kept in the notification center
//   toast("Saved", { kind: "success" });     // auto-dismisses, not kept
//   toast("Deleted", { kind: "success", persist: true }); // kept in the notification center
// Errors (role=alert) and any { persist:true } toast are logged to the notification center
// via toastLog so they can be reviewed after leaving the screen; trivial toasts (a copy
// confirmation and the like) stay purely ephemeral.
import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "./Icon.tsx";
import { useT } from "../lib/i18n/index.ts";
import { pushToastLog } from "../lib/toastLog.ts";
import { registerToastSink } from "./toast.ts";

export type ToastKind = "error" | "warn" | "info" | "success";

export interface ToastOptions {
  kind?: ToastKind;
  duration?: number; // ms; 0 = sticky (manual close only)
  persist?: boolean; // also keep in the notification center after dismiss (default: errors)
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

export const TOAST_ICONS: Record<ToastKind, string> = {
  error: "error",
  warn: "warning",
  info: "info",
  success: "pass",
};

export function ToastProvider({ children }: { children: ReactNode }) {
  const tr = useT();
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
      // Errors (and any opt-in persist) are recorded in the notification center so a failure
      // that scrolled away can still be reviewed. Only string messages can be logged.
      if ((opts?.persist ?? kind === "error") && typeof message === "string") pushToastLog(kind, message);
      // Errors auto-dismiss rather than sticking (0); the notification center keeps them.
      const duration = opts?.duration ?? (kind === "error" ? 8000 : 4000);
      if (duration > 0) setTimeout(() => remove(id), duration);
    },
    [remove],
  );

  // Expose this provider's toast to non-React callers (keyboard commands) while mounted.
  useEffect(() => {
    registerToastSink(toast);
    return () => registerToastSink(null);
  }, [toast]);

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
              <Icon name={TOAST_ICONS[t.kind]} />
              <span className="ui-toast-msg">{t.message}</span>
              <button type="button" className="ui-toast-x" title={tr("ui.close")} onClick={() => remove(t.id)}>
                <Icon name="close" />
              </button>
            </div>
          ))}
        </div>
      )}
    </ToastCtx.Provider>
  );
}
