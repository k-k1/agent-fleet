import { createPortal } from "react-dom";
import type { ReactNode, MouseEvent } from "react";
import { Button } from "./Button.tsx";
import { useEscLayer } from "../lib/escLayer.ts";

// ConfirmDialog: a small modal for confirming destructive actions (old
// components/ConfirmDialog). Renders a title, a body (string or nodes), and
// Cancel / Confirm buttons; the confirm button is destructive when `danger`.
// Unlike useConfirm (promise-based one-liner), this is a component the caller
// renders directly — used where the body is rich JSX (e.g. 設定 > 環境 作り直す).
interface ConfirmDialogProps {
  title?: ReactNode;
  children?: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  busy?: boolean;
  onConfirm?: () => void;
  onCancel?: () => void;
}

export function ConfirmDialog({
  title,
  children,
  confirmLabel = "実行",
  danger = true,
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  // Escape cancels (unless the operation is running) — layered, so the dialog
  // this confirm was opened from stays open.
  useEscLayer(onCancel, !busy);

  // Portal to <body> + ui-confirm-backdrop (higher z-index): callers render this
  // from inside a settings/admin dialog, so an in-tree overlay at the modal
  // z-index could paint behind the dialog it should cover.
  return createPortal(
    <div className="ui-modal-backdrop ui-confirm-backdrop" onClick={busy ? undefined : onCancel}>
      <div className="confirm" onClick={(e: MouseEvent) => e.stopPropagation()}>
        <h3 className="confirm-title">{title}</h3>
        <div className="confirm-body">{children}</div>
        <div className="confirm-actions">
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            キャンセル
          </Button>
          <Button variant={danger ? "danger" : "default"} onClick={onConfirm} disabled={busy}>
            {busy ? "実行中…" : confirmLabel}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
