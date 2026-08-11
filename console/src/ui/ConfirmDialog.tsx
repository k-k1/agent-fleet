import { useId, useRef } from "react";
import { createPortal } from "react-dom";
import type { ReactNode, MouseEvent } from "react";
import { Button } from "./Button.tsx";
import { useT } from "../lib/i18n/index.ts";
import { useEscLayer } from "../lib/escLayer.ts";
import { useBackClose } from "../lib/backClose.ts";
import { useFocusTrap } from "../lib/focusTrap.ts";

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
  confirmLabel,
  danger = true,
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const tr = useT();
  // Escape cancels (unless the operation is running) — layered, so the dialog
  // this confirm was opened from stays open.
  useEscLayer(onCancel, !busy);
  // 端末の戻る操作でもこの確認だけ剥がせるように（ConfirmProvider と同じ流儀）。
  // busy 中は Esc/backdrop 同様に無効。
  useBackClose(onCancel, !busy);
  // Trap Tab within the confirm; focus lands on the confirm button (data-autofocus
  // below) so Space/Enter fires it immediately — Esc is the escape hatch for
  // changing your mind. Returns focus to the opener on close.
  const ref = useRef<HTMLDivElement>(null);
  useFocusTrap(ref, true);
  const titleId = useId();

  // Portal to <body> + ui-confirm-backdrop (higher z-index): callers render this
  // from inside a settings/admin dialog, so an in-tree overlay at the modal
  // z-index could paint behind the dialog it should cover.
  return createPortal(
    <div className="ui-modal-backdrop ui-confirm-backdrop" onClick={busy ? undefined : onCancel}>
      <div className="confirm" ref={ref} role="dialog" aria-modal="true" aria-labelledby={titleId} onClick={(e: MouseEvent) => e.stopPropagation()}>
        <h3 className="confirm-title" id={titleId}>{title}</h3>
        <div className="confirm-body">{children}</div>
        <div className="confirm-actions">
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            {tr("ui.cancel")}
          </Button>
          <Button variant={danger ? "danger" : "default"} onClick={onConfirm} disabled={busy} data-autofocus>
            {busy ? tr("ui.running") : confirmLabel ?? tr("ui.run")}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
