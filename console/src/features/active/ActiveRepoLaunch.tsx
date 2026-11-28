// ActiveRepoLaunch — the Launch split button on an Active repository heading: the Repositories
// row's own LaunchSplit (▷ Launch opens the dialog, ▼ the per-kind quick menu). A heading stands
// for every local clone of one remote, so with several of them the choice of clone comes first
// for both parts, as a small menu of folder names.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useT } from "../../lib/i18n/index.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { LaunchKindItems, LaunchSplit } from "../repos/LaunchSplit.tsx";
import { useLaunchTarget, type Repo } from "../repos/store.ts";
import type { QuickLaunch } from "../repos/useQuickLaunch.ts";

/** What is open under the split button: the choice of clone (then = what the choice is for), or
 *  the per-kind menu of one clone. */
type Step = { menu: "clone"; then: "dialog" | "kinds" } | { menu: "kinds"; copy: string };

export interface ActiveRepoLaunchProps {
  label: string;
  /** The local checkouts of this remote, base clones only. */
  copies: Repo[];
  running: boolean;
  /** Kinds the ▼ menu offers (connection- and policy-gated, as on a Repositories row). */
  kinds: string[];
  /** At least one of them runs in a directory: without one the dialog has nothing to offer. */
  hasAgents: boolean;
  quickLaunch: QuickLaunch;
}

export function ActiveRepoLaunch({ label, copies, running, kinds, hasAgents, quickLaunch }: ActiveRepoLaunchProps) {
  const tr = useT();
  const [step, setStep] = useState<Step | null>(null);
  const split = useRef<HTMLDivElement>(null);
  const mainBtn = useRef<HTMLButtonElement>(null);
  const caretBtn = useRef<HTMLButtonElement>(null);
  // The button that opened the current menu: focus goes back to it when the menu closes.
  const opener = useRef<HTMLButtonElement | null>(null);
  const cloneMenu = useRef<HTMLUListElement>(null);
  const kindMenu = useRef<HTMLDivElement>(null);
  const multiple = copies.length > 1;
  const kindsCopy = step?.menu === "kinds" ? copies.find((c) => c.name === step.copy) : undefined;
  // The clone menu belongs to the part that opened it, so it closes when that part is disabled
  // (▷ needs an agent that runs in a directory, ▼ needs a kind to list), not only when it is gone.
  const cloneOpen = step?.menu === "clone" && running && multiple && (step.then === "dialog" ? hasAgents : kinds.length > 0);
  const kindsOpen = step?.menu === "kinds" && running && !!kindsCopy && kinds.length > 0;
  const open = cloneOpen || kindsOpen;
  const noCopy = copies.length === 0;
  const unavailable = !running ? tr("active.launch_stopped") : noCopy ? tr("active.launch_missing") : undefined;
  const close = () => {
    setStep(null);
    opener.current?.focus({ preventScroll: true });
  };
  // A menu whose conditions no longer hold (a clone went away, the workspace stopped) is dropped
  // rather than left to reopen when they come back.
  useEffect(() => {
    if (step && !open) setStep(null);
  }, [step, open]);
  useDismiss([split, cloneMenu, kindMenu], open, close);
  useLayoutEffect(() => {
    const menu = cloneMenu.current ?? kindMenu.current;
    if (!open || !split.current || !menu) return;
    const anchor = split.current.getBoundingClientRect();
    placeFixed(menu, anchor.right - menu.offsetWidth, anchor.bottom + 2, split.current.closest<HTMLElement>(".app-rail"));
  });
  useLayoutEffect(() => {
    if (!open) return;
    return () => {
      // Read when the menu closes, not when it opened: the member may have switched to the other part.
      const from = opener.current;
      // A catalogue update can remove the focused menu item and disable its opener at once.
      if (!from?.isConnected || document.activeElement !== document.body) return;
      const fallback = from.disabled ? from.closest(".act-gtitle")?.querySelector<HTMLButtonElement>(".act-gfold") : from;
      fallback?.focus({ preventScroll: true });
    };
  }, [open]);
  // One instance per menu: the roving focus starts when a menu opens, and the move from the clone
  // menu to the kind menu keeps `open` true.
  useMenuRoving(cloneMenu, cloneOpen);
  // Pressing the other part while a clone menu is open keeps `cloneOpen` true, so the roving hook
  // does not run again and the browser leaves focus on the pressed button: put it on the first
  // item of the menu that now answers.
  const cloneFor = step?.menu === "clone" ? step.then : "";
  useEffect(() => {
    if (cloneOpen) cloneMenu.current?.querySelector<HTMLElement>(".ui-menu-item")?.focus({ preventScroll: true });
  }, [cloneOpen, cloneFor]);
  useMenuRoving(kindMenu, kindsOpen);

  const openDialog = (copy: Repo) => {
    // The dialog is opened from ▷ whatever menu was open before: its focus-trap records the element
    // focused now as its opener, and a stale ▼ from an earlier menu would be where focus returns.
    opener.current = mainBtn.current;
    close();
    useLaunchTarget.getState().open(copy);
  };
  const pick = (copy: Repo, kind: string, newPane: boolean) => {
    close();
    void Promise.resolve(quickLaunch(copy, kind, newPane)).then((res) => {
      if (res === "dialog") useLaunchTarget.getState().open(copy, "", false, kind);
    });
  };
  const toggle = (from: HTMLButtonElement | null, next: Step, matches: (s: Step) => boolean) => {
    opener.current = from;
    setStep(step && open && matches(step) ? null : next);
  };
  // The caret's own menus: pressing it with the dialog's clone choice open switches to its menu.
  const forKinds = (s: Step) => s.menu === "kinds" || s.then === "kinds";
  const action = tr("active.launch_repo", { repo: label });
  const mainOpen = cloneOpen && step?.menu === "clone" && step.then === "dialog";
  const caretOpen = open && !mainOpen;
  return (
    <>
      <LaunchSplit
        running={running}
        hasAgents={hasAgents}
        unavailable={unavailable}
        caretDisabled={noCopy || kinds.length === 0}
        mainLabel={action}
        splitRef={split}
        mainRef={mainBtn}
        caretRef={caretBtn}
        mainProps={{
          "aria-description": unavailable,
          "aria-haspopup": multiple ? "menu" : "dialog",
          "aria-expanded": multiple ? mainOpen : undefined,
        }}
        caretProps={{
          "aria-label": tr("active.launch_quick_repo", { repo: label }),
          "aria-description": unavailable,
          "aria-haspopup": "menu",
          "aria-expanded": caretOpen,
        }}
        onMain={() => {
          if (multiple) toggle(mainBtn.current, { menu: "clone", then: "dialog" }, (s) => s.menu === "clone" && s.then === "dialog");
          else if (copies[0]) openDialog(copies[0]);
        }}
        onCaret={() => {
          if (multiple) toggle(caretBtn.current, { menu: "clone", then: "kinds" }, forKinds);
          else if (copies[0]) toggle(caretBtn.current, { menu: "kinds", copy: copies[0].name }, forKinds);
        }}
      />
      {cloneOpen && createPortal(
        <ul className="ui-menu act-launch-menu" ref={cloneMenu} role="menu" aria-label={tr("active.launch_choose")}
          onKeyDown={(e) => { if (e.key === "Tab") close(); }}>
          {copies.map((copy) => (
            <li key={copy.name} role="none">
              <button type="button" className="ui-menu-item" role="menuitem" tabIndex={-1}
                onClick={() => (step?.menu === "clone" && step.then === "kinds" ? setStep({ menu: "kinds", copy: copy.name }) : openDialog(copy))}>
                {copy.name}
              </button>
            </li>
          ))}
        </ul>,
        document.body,
      )}
      {kindsOpen && kindsCopy && createPortal(
        <div className="ui-menu launch-menu" ref={kindMenu} role="menu" aria-label={tr("repo.quick_launch")} onMouseDown={(e) => e.stopPropagation()}
          onKeyDown={(e) => { if (e.key === "Tab") close(); }}>
          <LaunchKindItems menu kinds={kinds} onPick={(kind, newPane) => pick(kindsCopy, kind, newPane)} />
        </div>,
        document.body,
      )}
    </>
  );
}
