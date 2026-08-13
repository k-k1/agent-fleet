// WorkingSetBar — the rail-top 作業グループ switcher (docs/52 + ADR 0036).
// Pinned above .app-rail-scroll (outside it, so it stays visible however far the
// rail scrolls — that visibility is the "why did my session disappear" guard)
// and rendered for the stopped rail too. The button shows the active group name
// (すべて when unscoped); the menu switches groups, the modal manages them.
// Assignment lives on the rows themselves (repo / conversation / session menus).
import { createPortal } from "react-dom";
import { memo, useLayoutEffect, useRef, useState } from "react";
import { Icon } from "../ui/Icon.tsx";
import { Modal } from "../ui/Modal.tsx";
import { Button, IconButton } from "../ui/Button.tsx";
import { useConfirm } from "../ui/ConfirmProvider.tsx";
import { useDismiss } from "../lib/useDismiss.ts";
import { useMenuRoving } from "../lib/useMenuRoving.ts";
import { placeFixed } from "../lib/placeFixed.ts";
import { useSettings } from "../lib/settings.ts";
import { useT } from "../lib/i18n/index.ts";
import {
  workingSetList,
  activeWorkingSet,
  setActiveWorkingSet,
  createWorkingSet,
  renameWorkingSet,
  deleteWorkingSet,
} from "../lib/workingSetsStore.ts";
import type { WorkingSet } from "../lib/workingSetsStore.ts";

export const WorkingSetBar = memo(function WorkingSetBar() {
  const tr = useT();
  const settings = useSettings();
  const sets = workingSetList(settings);
  const active = activeWorkingSet(settings);
  const [menuOpen, setMenuOpen] = useState(false);
  const [manageOpen, setManageOpen] = useState(false);
  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  useDismiss([btnRef, menuRef], menuOpen, () => setMenuOpen(false));
  useMenuRoving(menuRef, menuOpen);
  // Anchored under the bar button, viewport/rail-clamped — same pattern as the
  // session-row ⋯ menu (position:fixed so a long rail can't push it off-screen).
  useLayoutEffect(() => {
    const el = menuRef.current;
    const anchor = btnRef.current;
    if (!menuOpen || !el || !anchor) return;
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.left, a.bottom + 2, btnRef.current?.closest<HTMLElement>(".app-rail"));
  });
  const pick = (id: string) => {
    setMenuOpen(false);
    setActiveWorkingSet(id);
  };

  return (
    <div className="wset-bar">
      <button
        type="button"
        ref={btnRef}
        className={"wset-btn" + (active ? " scoped" : "")}
        title={tr("wset.bar_title")}
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        onClick={() => setMenuOpen((v) => !v)}
      >
        <Icon name="layers" />
        <span className="wset-name">{active ? active.name : tr("wset.all")}</span>
        <Icon name="chevron-down" className="wset-caret" />
      </button>
      {menuOpen &&
        createPortal(
          <ul className="ui-menu wset-menu" ref={menuRef} role="menu" onMouseDown={(e) => e.stopPropagation()}>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => pick("")}>
                <Icon name="check" className={active ? "wset-check off" : "wset-check"} /> {tr("wset.all")}
              </button>
            </li>
            {sets.map((w) => (
              <li key={w.id}>
                <button type="button" className="ui-menu-item" onClick={() => pick(w.id)}>
                  <Icon name="check" className={active?.id === w.id ? "wset-check" : "wset-check off"} /> {w.name}
                </button>
              </li>
            ))}
            <li className="ui-menu-sep" role="separator" />
            <li>
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  setMenuOpen(false);
                  setManageOpen(true);
                }}
              >
                <Icon name="settings-gear" /> {tr("wset.manage")}
              </button>
            </li>
          </ul>,
          document.body,
        )}
      {manageOpen && <WorkingSetManageModal onClose={() => setManageOpen(false)} />}
    </div>
  );
});

// Manage modal: create / rename (inline, commit on blur or Enter) / delete.
// Deleting removes only the group definition — members are never touched
// (docs/52 §3), which is why delete needs just a light confirm.
function WorkingSetManageModal({ onClose }: { onClose: () => void }) {
  const tr = useT();
  const settings = useSettings();
  const sets = workingSetList(settings);
  const [newName, setNewName] = useState("");
  const create = () => {
    const n = newName.trim();
    if (!n) return;
    // Creating from the manage modal also switches to the new (empty) group —
    // the natural next step is assigning rows to it, and the scoped-empty rail
    // makes the "assign from row menus" affordance discoverable.
    setActiveWorkingSet(createWorkingSet(n));
    setNewName("");
  };
  return (
    <Modal title={tr("wset.manage_title")} onClose={onClose}>
      <div className="ui-modal-body wset-manage">
        {sets.length === 0 && <p className="wset-empty-hint">{tr("wset.empty_hint")}</p>}
        {sets.length > 0 && (
          <ul className="wset-list">
            {sets.map((w) => (
              <WorkingSetRow key={w.id} w={w} />
            ))}
          </ul>
        )}
        <form
          className="wset-new"
          onSubmit={(e) => {
            e.preventDefault();
            create();
          }}
        >
          <input
            type="text"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            placeholder={tr("wset.new_ph")}
            maxLength={40}
            autoFocus
          />
          <Button small icon="add" type="submit" disabled={!newName.trim()}>
            {tr("wset.create")}
          </Button>
        </form>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("common.close")}
        </Button>
      </footer>
    </Modal>
  );
}

function WorkingSetRow({ w }: { w: WorkingSet }) {
  const tr = useT();
  const askConfirm = useConfirm();
  const [name, setName] = useState(w.name);
  const commit = () => {
    const n = name.trim();
    if (!n) {
      setName(w.name); // an emptied name reverts — a group must stay addressable
      return;
    }
    if (n !== w.name) renameWorkingSet(w.id, n);
  };
  const del = async () => {
    const ok = await askConfirm({
      title: tr("wset.delete_title"),
      body: tr("wset.delete_confirm", { name: w.name }),
      confirmLabel: tr("common.delete_do"),
      danger: true,
    });
    if (ok) deleteWorkingSet(w.id);
  };
  return (
    <li className="wset-row">
      <Icon name="layers" />
      <input
        type="text"
        value={name}
        onChange={(e) => setName(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            commit();
          }
        }}
        maxLength={40}
        aria-label={tr("wset.name_aria")}
      />
      <span
        className="wset-counts"
        title={tr("wset.row_counts", {
          repos: w.repos.length,
          convs: w.convs.length,
          sessions: w.sessions.length,
          schedules: w.schedules.length,
        })}
      >
        <Icon name="repo" />
        {w.repos.length}
        <Icon name="comment-discussion" />
        {w.convs.length}
        {w.schedules.length > 0 && (
          <>
            <Icon name="watch" />
            {w.schedules.length}
          </>
        )}
        {w.sessions.length > 0 && (
          <>
            <Icon name="terminal" />
            {w.sessions.length}
          </>
        )}
      </span>
      <IconButton icon="trash" label={tr("wset.delete")} onClick={() => void del()} />
    </li>
  );
}
