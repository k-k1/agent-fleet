// タブ切替(docs/log/53 §53.??): 複数タブを順に操作するスクリプトへ attach したとき、
// 別タブへ切り替えるたびに「ペインを閉じる→エージェントに再attachを頼む→新しいリンクを
// 開く」を繰り返すのは UX を損ねる。同じ attachment id のまま裏の CDP target だけ差し替える
// Retarget API をこのボタンから直接呼べるようにする。
import { createPortal } from "react-dom";
import { useLayoutEffect, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useT } from "../../lib/i18n/index.ts";
import type { BrowserAttachmentSiblingTarget } from "./attachmentController.ts";

interface BrowserAttachTargetSwitcherProps {
  disabled?: boolean;
  listSiblingTargets: () => Promise<BrowserAttachmentSiblingTarget[]>;
  onSelect: (targetId: string) => Promise<void>;
}

/** Origin-ish label for a candidate row: falls back to the raw URL if it does not parse. */
function shortLabel(url: string): string {
  try {
    const parsed = new URL(url);
    return parsed.hostname + parsed.pathname;
  } catch {
    return url;
  }
}

export function BrowserAttachTargetSwitcher({ disabled, listSiblingTargets, onSelect }: BrowserAttachTargetSwitcherProps) {
  const tr = useT();
  const wrapRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [targets, setTargets] = useState<BrowserAttachmentSiblingTarget[] | null>(null);
  const [error, setError] = useState(false);
  const [switching, setSwitching] = useState<string | null>(null);

  useDismiss([wrapRef, menuRef], open, () => setOpen(false));
  useMenuRoving(menuRef, open);
  useLayoutEffect(() => {
    const el = menuRef.current;
    const anchor = btnRef.current;
    if (!open || !el || !anchor) return;
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.left, a.bottom + 2);
  });

  const load = () => {
    setTargets(null);
    setError(false);
    listSiblingTargets()
      .then((list) => setTargets(list))
      .catch(() => setError(true));
  };

  const select = (target: BrowserAttachmentSiblingTarget) => {
    if (switching) return;
    setSwitching(target.targetId);
    onSelect(target.targetId)
      .then(() => setOpen(false))
      .catch(() => {
        // The pane's own error toast already reports this; keep the menu open
        // so the user can just try a different tab instead of reopening it.
      })
      .finally(() => setSwitching(null));
  };

  return (
    <div className="browser-attach-switch-wrap" ref={open ? wrapRef : undefined}>
      <button
        type="button"
        className="ui-btn ui-btn-ghost ui-iconbtn browser-attach-switch-btn"
        aria-label={tr("browser.attach.switch_tab")}
        title={tr("browser.attach.switch_tab")}
        disabled={disabled}
        ref={btnRef}
        onClick={() => {
          const next = !open;
          setOpen(next);
          if (next) load();
        }}
      >
        <Icon name="multiple-windows" />
      </button>
      {open &&
        createPortal(
          <div className="ui-menu browser-attach-switch-menu" ref={menuRef} onMouseDown={(e) => e.stopPropagation()}>
            {targets === null && !error && <div className="ui-menu-caption">{tr("browser.attach.switch_tab_loading")}</div>}
            {error && <div className="ui-menu-caption">{tr("browser.attach.switch_tab_failed")}</div>}
            {/* The current target is the toolbar's own title/url, so the picker only
                lists what a click could actually change to. */}
            {targets !== null && targets.every((target) => target.current) && (
              <div className="ui-menu-caption">{tr("browser.attach.switch_tab_empty")}</div>
            )}
            {targets?.filter((target) => !target.current).map((target) => (
              <button
                key={target.targetId}
                type="button"
                className="ui-menu-item browser-attach-switch-item"
                disabled={switching !== null}
                onClick={() => select(target)}
              >
                <Icon name={switching === target.targetId ? "loading" : "browser"} spin={switching === target.targetId} />
                <span className="browser-attach-switch-item-text">
                  <strong>{target.title || tr("pane.kind.browser_attach")}</strong>
                  <small>{shortLabel(target.url)}</small>
                </span>
              </button>
            ))}
          </div>,
          document.body,
        )}
    </div>
  );
}
