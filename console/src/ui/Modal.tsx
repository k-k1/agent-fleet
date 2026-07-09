// Modal — the shared dialog shell: backdrop, centered panel, header with title +
// close. Backdrop click / Escape close unless `lockClose` (an operation in
// flight — the panel goes inert and the close button becomes a spinner). Pass
// `as="form"` + onSubmit for form dialogs. Port of the old components/Modal.
import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import type { ReactNode, MouseEvent, FormEvent } from "react";
import { Icon } from "./Icon.tsx";
import { IconButton } from "./Button.tsx";
import { coarsePointer } from "../lib/device.ts";
import { useEscLayer } from "../lib/escLayer.ts";

interface ModalProps {
  title?: ReactNode;
  onClose?: () => void;
  className?: string;
  as?: "div" | "form";
  onSubmit?: (e: FormEvent) => void;
  lockClose?: boolean;
  children?: ReactNode;
}

export function Modal({
  title,
  onClose,
  className = "",
  as = "div",
  onSubmit,
  lockClose = false,
  children,
}: ModalProps) {
  // Esc closes (unless an operation is in flight) — layered, so with a confirm
  // dialog open above this modal, Esc peels the confirm first, not both at once.
  useEscLayer(onClose, !lockClose);

  // On touch devices, undo any child's autoFocus once the panel mounts so opening a
  // dialog doesn't pop the soft keyboard (GBoard) — the user usually reviews/taps
  // before typing. autoFocus is applied synchronously on mount, so this effect (which
  // runs after) blurs whatever landed inside. Desktop keeps autofocus for keyboard flow.
  const panelRef = useRef<HTMLElement>(null);
  useEffect(() => {
    if (!coarsePointer()) return;
    const el = document.activeElement as HTMLElement | null;
    if (el && panelRef.current?.contains(el)) el.blur();
  }, []);

  const Panel = as;
  const panelProps: Record<string, unknown> = {
    ref: panelRef,
    className: ("ui-modal " + className).trim(),
    onClick: (e: MouseEvent) => e.stopPropagation(),
  };
  if (as === "form") panelProps.onSubmit = onSubmit;
  // React 19 treats `inert` as a boolean prop.
  if (lockClose) panelProps.inert = true;
  // Portal to <body>: many modals are mounted deep inside the left rail sections,
  // and on mobile the rail is an off-canvas drawer with `transform: translateX()`.
  // A transformed ancestor becomes the containing block for position:fixed, which
  // would trap the backdrop/panel inside the ~320px drawer instead of the viewport.
  // Rendering at body decouples the modal from wherever it's invoked. Context
  // (stores / toast / confirm) still flows through — portals preserve the React tree.
  return createPortal(
    // Stop contextmenu bubbling past the backdrop: a modal may render inside an
    // element with its own onContextMenu (e.g. a repo row's right-click menu).
    <div
      className="ui-modal-backdrop"
      onClick={lockClose ? undefined : onClose}
      onContextMenu={(e) => e.stopPropagation()}
    >
      <Panel {...panelProps}>
        <header className="ui-modal-head">
          <h3 className="ui-modal-title">{title}</h3>
          {lockClose ? (
            <Icon name="loading" spin title="処理中…" />
          ) : (
            <IconButton icon="close" label="閉じる" onClick={onClose} />
          )}
        </header>
        {children}
      </Panel>
    </div>,
    document.body,
  );
}
