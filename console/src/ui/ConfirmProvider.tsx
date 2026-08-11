// Promise-based confirmation replacing the blocking native window.confirm():
//   if (!(await askConfirm({ title, body, danger }))) return;
// The provider mounts a single dialog and resolves the pending promise when the
// user chooses. Port of the old ConfirmProvider + ConfirmDialog (merged — the
// standalone dialog had no other consumer).
import { createContext, useCallback, useContext, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { ReactNode, MouseEvent } from "react";
import { Button } from "./Button.tsx";
import { useT } from "../lib/i18n/index.ts";
import { useEscLayer } from "../lib/escLayer.ts";
import { useBackClose } from "../lib/backClose.ts";
import { useFocusTrap } from "../lib/focusTrap.ts";
import { registerConfirm } from "./confirmBridge.ts";

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
  const tr = useT();
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

  // Escape cancels, matching the native confirm() this replaced — layered, so
  // the modal the confirm was asked from stays open.
  useEscLayer(() => finish(false), !!req);
  // Back button / back-swipe also cancels, and peels the confirm before the
  // modal beneath it (both push a layered history guard).
  useBackClose(() => finish(false), !!req);
  // Trap Tab within the confirm while it's open; focus lands on the confirm button
  // (data-autofocus below) so Space/Enter fires it immediately — Esc is the escape
  // hatch for changing your mind. Restore focus to the opener on close.
  const dialogRef = useRef<HTMLDivElement>(null);
  useFocusTrap(dialogRef, !!req);
  const titleId = useId();

  // Expose this provider imperatively so non-React code (terminal/term.ts) can confirm.
  useEffect(() => {
    registerConfirm(askConfirm);
    return () => registerConfirm(null);
  }, [askConfirm]);

  return (
    <ConfirmCtx.Provider value={askConfirm}>
      {children}
      {req &&
        // Portal to <body> + ui-confirm-backdrop (higher z-index): a confirm is
        // usually asked from inside a Modal, which itself portals to <body>. In-tree
        // rendering would leave this overlay earlier in the DOM at the same z-index,
        // painting it BEHIND the modal.
        createPortal(
          <div className="ui-modal-backdrop ui-confirm-backdrop" onClick={() => finish(false)}>
            <div className="ui-confirm" ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby={titleId} onClick={(e: MouseEvent) => e.stopPropagation()}>
              <h3 className="ui-confirm-title" id={titleId}>{req.title}</h3>
              <div className="ui-confirm-body">{req.body}</div>
              <div className="ui-confirm-actions">
                <Button variant="ghost" onClick={() => finish(false)}>
                  {tr("ui.cancel")}
                </Button>
                <Button variant={req.danger ?? true ? "danger" : "primary"} onClick={() => finish(true)} data-autofocus>
                  {req.confirmLabel || tr("ui.run")}
                </Button>
              </div>
            </div>
          </div>,
          document.body,
        )}
    </ConfirmCtx.Provider>
  );
}
