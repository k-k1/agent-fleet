import { useEffect } from "react";
import type { ReactNode, MouseEvent, FormEvent } from "react";
import Icon from "./Icon.jsx";

// Modal is the shared dialog shell: a backdrop, a centered panel, and a header with
// a title + close button. Every modal in the app used to repeat this markup. The
// backdrop closes on click unless `lockClose` (e.g. a clone/checkout in flight).
// Pass `as="form"` + `onSubmit` for form dialogs; `className` adds a size variant
// (session-modal / branch-modal / settings-modal). Children are the body/footer.
interface ModalProps {
  title?: ReactNode;
  onClose?: () => void;
  className?: string;
  as?: "div" | "form";
  onSubmit?: (e: FormEvent) => void;
  lockClose?: boolean;
  children?: ReactNode;
}

export default function Modal({
  title,
  onClose,
  className = "",
  as = "div",
  onSubmit,
  lockClose = false,
  children,
}: ModalProps) {
  // Esc closes the dialog (unless an operation is in flight). Registered at the
  // document so it works regardless of where focus sits inside the panel.
  useEffect(() => {
    if (!onClose || lockClose) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose, lockClose]);

  const Panel = as;
  const panelProps: Record<string, unknown> = {
    className: ("modal " + className).trim(),
    onClick: (e: MouseEvent) => e.stopPropagation(),
  };
  if (as === "form") panelProps.onSubmit = onSubmit;
  // While an operation is in flight (lockClose), make the whole panel inert so
  // nothing inside is operable, and the backdrop click is ignored. The close
  // button is replaced by a spinner so there's no way to dismiss mid-op.
  // React 19 treats `inert` as a boolean prop; an empty string is falsy and would
  // NOT apply it (leaving the panel operable mid-op), so pass `true`.
  if (lockClose) panelProps.inert = true;
  return (
    // Stop contextmenu from bubbling past the backdrop: a modal may be rendered inside
    // an element with its own onContextMenu (e.g. a repo row's right-click menu), and a
    // long-press / right-click inside the dialog (notably a textarea on mobile) must not
    // trigger that underlying menu. Not prevented — the field's native menu still works.
    <div className="modal-backdrop" onClick={lockClose ? undefined : onClose} onContextMenu={(e) => e.stopPropagation()}>
      <Panel {...panelProps}>
        <header className="modal-head">
          <h3 className="modal-title">{title}</h3>
          {lockClose ? (
            <Icon name="loading" spin className="modal-busy" title="処理中…" />
          ) : (
            <button type="button" className="icon" title="閉じる" onClick={onClose}>
              <Icon name="close" />
            </button>
          )}
        </header>
        {children}
      </Panel>
    </div>
  );
}
