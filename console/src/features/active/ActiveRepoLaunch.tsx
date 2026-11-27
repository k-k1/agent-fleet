import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useT } from "../../lib/i18n/index.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { Button } from "../../ui/Button.tsx";
import { useLaunchTarget, type Repo } from "../repos/store.ts";

export function ActiveRepoLaunch({ label, copies, running }: { label: string; copies: Repo[]; running: boolean }) {
  const tr = useT();
  const [choosing, setChoosing] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLUListElement>(null);
  const multiple = copies.length > 1;
  const open = choosing && running && multiple;
  const disabled = !running || copies.length === 0;
  const unavailable = disabled ? tr(!running ? "active.launch_stopped" : "active.launch_missing") : undefined;
  const close = () => {
    setChoosing(false);
    trigger.current?.focus({ preventScroll: true });
  };
  useEffect(() => {
    if (!running || !multiple) setChoosing(false);
  }, [running, multiple]);
  useDismiss([trigger, menu], open, close);
  useLayoutEffect(() => {
    if (!open || !trigger.current || !menu.current) return;
    const anchor = trigger.current.getBoundingClientRect();
    placeFixed(menu.current, anchor.right - menu.current.offsetWidth, anchor.bottom + 2, trigger.current.closest<HTMLElement>(".app-rail"));
  });
  useLayoutEffect(() => {
    if (!open) return;
    const opener = trigger.current;
    return () => {
      // A catalogue update can remove the focused menu item and disable its opener at once.
      if (!opener?.isConnected || document.activeElement !== document.body) return;
      const fallback = opener.disabled ? opener.closest(".act-gtitle")?.querySelector<HTMLButtonElement>(".act-gfold") : opener;
      fallback?.focus({ preventScroll: true });
    };
  }, [open]);
  useMenuRoving(menu, open);

  const launch = (copy: Repo) => {
    close();
    useLaunchTarget.getState().open(copy);
  };
  const action = tr("active.launch_repo", { repo: label });
  return (
    <>
      <Button small className="act-launch" ref={trigger} disabled={disabled}
        aria-label={action} aria-description={unavailable} title={unavailable || action}
        aria-haspopup={multiple ? "menu" : "dialog"} aria-expanded={multiple ? open : undefined}
        onClick={() => multiple ? setChoosing(!open) : launch(copies[0])}>
        {tr("active.launch")}
      </Button>
      {open && createPortal(
        <ul className="ui-menu act-launch-menu" ref={menu} role="menu" aria-label={tr("active.launch_choose")}
          onKeyDown={(e) => { if (e.key === "Tab") close(); }}>
          {copies.map((copy) => (
            <li key={copy.name} role="none">
              <button type="button" className="ui-menu-item" role="menuitem" tabIndex={-1} onClick={() => launch(copy)}>{copy.name}</button>
            </li>
          ))}
        </ul>,
        document.body,
      )}
    </>
  );
}
