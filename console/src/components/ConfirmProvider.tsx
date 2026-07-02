import { createContext, useCallback, useContext, useRef, useState } from "react";
import type { ReactNode } from "react";
import ConfirmDialog from "./ConfirmDialog.jsx";

// A promise-based confirmation, so scattered handlers can replace the blocking
// native window.confirm() with the styled ConfirmDialog by awaiting one call:
//   if (!(await askConfirm({ title, body, danger }))) return;
// The provider mounts a single ConfirmDialog and resolves the pending promise
// true/false when the user chooses. Reversible actions pass danger: false to get
// a neutral confirm button; destructive ones keep the default red styling.
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

export default function ConfirmProvider({ children }: { children: ReactNode }) {
  const [req, setReq] = useState<ConfirmOptions | null>(null);
  // The resolver for the promise handed back by the current askConfirm() call.
  const resolver = useRef<((v: boolean) => void) | null>(null);

  const askConfirm = useCallback<ConfirmFn>((opts) => {
    return new Promise<boolean>((resolve) => {
      // If a prior request somehow never settled, resolve it false before replacing.
      resolver.current?.(false);
      resolver.current = resolve;
      setReq(opts);
    });
  }, []);

  const finish = (v: boolean) => {
    resolver.current?.(v);
    resolver.current = null;
    setReq(null);
  };

  return (
    <ConfirmCtx.Provider value={askConfirm}>
      {children}
      {req && (
        <ConfirmDialog
          title={req.title}
          confirmLabel={req.confirmLabel}
          danger={req.danger ?? true}
          onConfirm={() => finish(true)}
          onCancel={() => finish(false)}
        >
          {req.body}
        </ConfirmDialog>
      )}
    </ConfirmCtx.Provider>
  );
}
