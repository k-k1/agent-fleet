// ConfirmDialog: a small modal for confirming destructive actions. Renders a
// title, a body (string or nodes), and Cancel / Confirm buttons. The confirm
// button is styled as destructive when `danger` is set.
export default function ConfirmDialog({ title, children, confirmLabel = "実行", danger = true, busy = false, onConfirm, onCancel }) {
  return (
    <div className="modal-backdrop" onClick={busy ? undefined : onCancel}>
      <div className="confirm" onClick={(e) => e.stopPropagation()}>
        <h3 className="confirm-title">{title}</h3>
        <div className="confirm-body">{children}</div>
        <div className="confirm-actions">
          <button className="ghost" onClick={onCancel} disabled={busy}>
            キャンセル
          </button>
          <button className={danger ? "danger-btn" : ""} onClick={onConfirm} disabled={busy}>
            {busy ? "実行中…" : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
