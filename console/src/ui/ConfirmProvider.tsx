// Promise-based confirmation replacing the blocking native window.confirm():
//   if (!(await askConfirm({ title, body, danger }))) return;
// The provider mounts a single dialog and resolves the pending promise when the
// user chooses. Port of the old ConfirmProvider + ConfirmDialog (merged — the
// standalone dialog had no other consumer).
import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import type { ReactNode, MouseEvent } from "react";
import { Button } from "./Button.tsx";

export interface ConfirmOptions {
  title?: ReactNode;
  body?: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
}

type ConfirmFn = (opts: ConfirmOptions) => Promise<boolean>;

const ConfirmCtx = createContext<ConfirmFn | null>(null);

export function useConfirm(): ConfirmFn {
  const fn = useContext(ConfirmCtx);
  if (!fn) throw new Error("useConfirm must be used within <ConfirmProvider>");
  return fn;
}

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [req, setReq] = useState<ConfirmOptions | null>(null);
  const resolver = useRef<((v: boolean) => void) | null>(null);

  const askConfirm = useCallback<ConfirmFn>((opts) => {
    return new Promise<boolean>((resolve) => {
      resolver.current?.(false); // a prior request that never settled → false
      resolver.current = resolve;
      setReq(opts);
    });
  }, []);

  const finish = useCallback((v: boolean) => {
    resolver.current?.(v);
    resolver.current = null;
    setReq(null);
  }, []);

  // Escape cancels, matching the native confirm() this replaced.
  useEffect(() => {
    if (!req) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") finish(false);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [req, finish]);

  return (
    <ConfirmCtx.Provider value={askConfirm}>
      {children}
      {req && (
        <div className="ui-modal-backdrop" onClick={() => finish(false)}>
          <div className="ui-confirm" onClick={(e: MouseEvent) => e.stopPropagation()}>
            <h3 className="ui-confirm-title">{req.title}</h3>
            <div className="ui-confirm-body">{req.body}</div>
            <div className="ui-confirm-actions">
              <Button variant="ghost" onClick={() => finish(false)}>
                キャンセル
              </Button>
              <Button variant={req.danger ?? true ? "danger" : "primary"} onClick={() => finish(true)}>
                {req.confirmLabel || "実行"}
              </Button>
            </div>
          </div>
        </div>
      )}
    </ConfirmCtx.Provider>
  );
}
