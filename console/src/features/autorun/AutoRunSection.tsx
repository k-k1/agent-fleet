// The Auto-run view (ADR 0140, note of 2026-11-21): one compact row per run of a memo auto-run
// queue, grouped by the queue's category. Unattended work is listed here and not in the
// repository tree. A row opens the run's session; the sessions the run started (reviewers) sit
// one level under it, and finished runs fold into "Ended N".
import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { KeyboardEvent, MouseEvent } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { Section } from "../../ui/Section.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { usePublishedHeight } from "../../lib/usePublishedHeight.ts";
import { useActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { useLeftRail } from "../../core/store/leftRail.ts";
import { useMemoStore } from "../memo/store.ts";
import { useRunNow } from "../memo/runNow.ts";
import { MemoQueueSettingsModal } from "../memo/MemoQueueSettingsModal.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { displayName } from "../../lib/sessionview.ts";
import { useViewShown } from "../../lib/viewShown.ts";
import { raw, errText, sessionDelete } from "../../core/api/client.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { askConfirm } from "../../ui/confirmBridge.ts";
import { toast } from "../../ui/toast.ts";
import { agentOf } from "../../agents/registry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { useReposStore } from "../repos/store.ts";
import { StopSessionsModal } from "../repos/StopSessionsModal.tsx";
import { DeleteCopyModal } from "../repos/DeleteCopyModal.tsx";
import type { RepoTreeNode } from "../../lib/project.ts";
import type { MemoQueue } from "../../types/memo.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { SessionMenu } from "../sessions/SessionMenu.tsx";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { isContextMenuKey, menuAnchor } from "../project/contextMenuKey.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import type { Session } from "../../types/session.ts";
import { openSessionChat } from "../sessions/open.ts";
import { refreshAutoRuns, reloadAutoRuns, useAutoRunFeed, useScopedAutoRuns } from "./store.ts";
import { buildRuns, fmtElapsed, scopeRuns, waitsForMember, type RunRow } from "./runs.ts";
import { ancestorNames, planClearFinished, runStopScope, runWorktreeNode } from "./runActions.ts";
import "./autorun.css";

const STATE_KEY = { running: "autorun.state_running", waiting: "autorun.state_waiting", ended: "autorun.state_ended" } as const;

const PR_KEY = { open: "autorun.pr_open", merged: "autorun.pr_merged", closed: "autorun.pr_closed" } as const;

/** What an open dialog acts on, frozen when the menu item is chosen. The view owns the dialog
 *  (not the row) so it survives the row moving to "Ended" or the list refreshing under it. */
type RunDialog = { kind: "stop"; node: RepoTreeNode; only: Set<string> } | { kind: "delete"; node: RepoTreeNode };

/** Only one of the view's menus is open at a time. The menus dismiss on a pointer press or Escape,
 *  not on keyboard focus moving, so opening one announces itself and the others close. */
const MENU_OPENED = "autorun-menu-opened";
const announceMenu = (who: string) => window.dispatchEvent(new CustomEvent(MENU_OPENED, { detail: who }));
function useCloseOnOtherMenu(who: string, close: () => void) {
  const closeRef = useRef(close);
  closeRef.current = close;
  useEffect(() => {
    const h = (e: Event) => {
      if ((e as CustomEvent<string>).detail !== who) closeRef.current();
    };
    window.addEventListener(MENU_OPENED, h);
    return () => window.removeEventListener(MENU_OPENED, h);
  }, [who]);
}

/** Opens the session menu for one session at a screen point. The view owns the menu itself. */
type OpenSessionMenu = (s: Session, at: { x: number; y: number }) => void;

/** Right-click and the Menu key / Shift+F10 on a row button open the session menu for that row's
 *  session. Not on the li: the row's own "..." button must keep its run menu to itself. */
function menuHandlers(s: Session, openMenu: OpenSessionMenu) {
  return {
    onContextMenu: (e: MouseEvent<HTMLElement>) => {
      e.preventDefault();
      openMenu(s, { x: e.clientX, y: e.clientY });
    },
    onKeyDown: (e: KeyboardEvent<HTMLElement>) => {
      if (e.target !== e.currentTarget || !isContextMenuKey(e)) return;
      e.preventDefault();
      openMenu(s, menuAnchor(e.currentTarget));
    },
  };
}

/** The row's "..." menu: stop the run's sessions, delete its worktree. Both open the repository
 *  tree's own plan dialogs, which carry the confirmation. */
function RunMenu({ run, onDialog }: { run: RunRow; onDialog: (d: RunDialog) => void }) {
  const tr = useT();
  const repos = useReposStore((s) => s.repos);
  const [at, setAt] = useState<{ x: number; y: number } | null>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  const stop = runStopScope(run, repos);
  const sessions = useSessionsStore((s) => s.sessions);
  const wt = runWorktreeNode(run, repos, sessions);
  useDismiss([btnRef, menuRef], !!at, () => setAt(null));
  useCloseOnOtherMenu("run:" + run.root.name, () => setAt(null));
  useMenuRoving(menuRef, !!at);
  useLayoutEffect(() => {
    if (at && menuRef.current) placeFixed(menuRef.current, at.x, at.y);
  });
  const open = () => {
    announceMenu("run:" + run.root.name);
    const r = btnRef.current?.getBoundingClientRect();
    setAt(r ? { x: r.left, y: r.bottom } : { x: 0, y: 0 });
  };
  return (
    <>
      <button
        type="button"
        ref={btnRef}
        className="autorun-run-menu"
        aria-label={tr("autorun.menu")}
        aria-haspopup="menu"
        aria-expanded={!!at}
        title={tr("autorun.menu")}
        onClick={() => (at ? setAt(null) : open())}
      >
        <Icon name="ellipsis" />
      </button>
      {at &&
        createPortal(
          <ul className="ui-menu" ref={menuRef} style={{ left: at.x, top: at.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
            <li>
              <button type="button" className="ui-menu-item" disabled={!stop} onClick={() => { setAt(null); if (stop) onDialog({ kind: "stop", ...stop }); }}>
                <Icon name="debug-stop" /> {tr("autorun.stop_run")}
              </button>
            </li>
            {wt && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setAt(null); if (wt) onDialog({ kind: "delete", node: wt }); }}>
                  <Icon name="trash" /> {tr("autorun.delete_worktree")}
                </button>
              </li>
            )}
          </ul>,
          document.body,
        )}
    </>
  );
}

function RunItem({ run, now, onDialog, openMenu, activeName }: { run: RunRow; now: number; onDialog: (d: RunDialog) => void; openMenu: OpenSessionMenu; activeName: string | null }) {
  const tr = useT();
  const { root, item, pr, children } = run;
  const end = run.state === "ended" ? run.endedAt : now;
  const elapsed = Number.isFinite(run.startedAt) && Number.isFinite(end) ? fmtElapsed(end - run.startedAt) : "";
  const title = item ? `${item.label} ${item.title || displayName(root)}`.trim() : displayName(root);
  return (
    <li className={"autorun-run is-" + run.state} data-autorun-run={root.name} data-autorun-state={run.state}>
      <button
        type="button"
        className={"autorun-run-main" + (root.name === activeName ? " active" : "")}
        aria-current={root.name === activeName ? "true" : undefined}
        data-rail-row=""
        onClick={() => openSessionChat(root.name)}
        {...menuHandlers(root, openMenu)}
        title={tr("autorun.open", { name: root.name })}
      >
        <span className={"autorun-dot is-" + run.state} role="img" aria-label={tr(STATE_KEY[run.state])} title={tr(STATE_KEY[run.state])} />
        <span className="autorun-title">{title}</span>
        {run.resumed && (
          <span className="autorun-resumed" title={tr("autorun.resumed_hint")}>
            {tr("autorun.resumed")}
          </span>
        )}
        {pr && (
          <span className={"autorun-pr " + (pr.draft && pr.state === "open" ? "draft" : pr.state)} title={tr("autorun.pr_title", { n: pr.number })}>
            #{pr.number} {pr.draft && pr.state === "open" ? tr("autorun.pr_draft") : tr(PR_KEY[pr.state])}
          </span>
        )}
        {elapsed && <span className="autorun-elapsed">{elapsed}</span>}
      </button>
      <RunMenu run={run} onDialog={onDialog} />
      {children.length > 0 && (
        <ul className="autorun-kids">
          {children.map((c) => (
            <li key={c.name}>
              <button type="button" className={"autorun-kid" + (c.name === activeName ? " active" : "")} aria-current={c.name === activeName ? "true" : undefined} data-rail-row="" onClick={() => openSessionChat(c.name)} {...menuHandlers(c, openMenu)} title={tr("autorun.open", { name: c.name })}>
                <span className={"autorun-dot is-" + (waitsForMember(c) ? "waiting" : c.alive ? "running" : "ended")} aria-hidden="true" />
                <span className="autorun-title">{displayName(c)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

/** The "..." menu of a category head: "Run now", the run history and the category's place in the
 *  Memo queue view, the last two of which that view already owns. */
function CategoryMenu({ categoryId, queue }: { categoryId: string; queue: MemoQueue | undefined }) {
  const tr = useT();
  const run = useRunNow(categoryId, queue);
  const [at, setAt] = useState<{ x: number; y: number } | null>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  useDismiss([btnRef, menuRef], !!at, () => setAt(null));
  useMenuRoving(menuRef, !!at);
  useLayoutEffect(() => {
    if (at && menuRef.current) placeFixed(menuRef.current, at.x, at.y);
  });
  const reveal = (history: boolean, slotsFull = false) => {
    setAt(null);
    useLeftRail.getState().showView("memo");
    useMemoStore.getState().revealCategory(categoryId, history, slotsFull);
  };
  return (
    <>
      <button
        type="button"
        ref={btnRef}
        className="autorun-cat-act"
        aria-label={tr("autorun.cat_menu")}
        aria-haspopup="menu"
        aria-expanded={!!at}
        title={tr("autorun.cat_menu")}
        onClick={() => {
          const r = btnRef.current?.getBoundingClientRect();
          setAt(at ? null : r ? { x: r.left, y: r.bottom } : { x: 0, y: 0 });
        }}
      >
        <Icon name="ellipsis" />
      </button>
      {at &&
        createPortal(
          <ul className="ui-menu" ref={menuRef} style={{ left: at.x, top: at.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
            <li>
              <button
                type="button"
                className="ui-menu-item"
                title={run.title}
                aria-label={run.title}
                // aria-disabled, not disabled: the roving focus skips a native disabled item, which
                // would leave the reason unreachable by keyboard.
                aria-disabled={run.reason !== "" || run.busy ? true : undefined}
                onClick={() => {
                  if (run.reason !== "" || run.busy) return;
                  setAt(null);
                  void run.press().then((r) => {
                    // The list behind the rows follows the start without waiting for its own poll.
                    if (r === "started") reloadAutoRuns();
                    // A slot was taken since the list was read: the history names who holds them.
                    if (r === "no_slot") reveal(true, true);
                  });
                }}
              >
                <Icon name="play" /> {tr("memo.d_fire")}
              </button>
            </li>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => reveal(true)}>
                <Icon name="history" /> {tr("memo.autorun_deliveries")}
              </button>
            </li>
            <li>
              <button type="button" className="ui-menu-item" onClick={() => reveal(false)}>
                <Icon name="inbox" /> {tr("autorun.show_in_memo")}
              </button>
            </li>
          </ul>,
          document.body,
        )}
    </>
  );
}

/** A sub-header of a category ("Running / waiting N", "Ended N"). It pins under the category
 *  header inside its own group, so the next group's header takes its place as the list scrolls. */
function SubHead({ label, open, onToggle, kind }: { label: string; open: boolean; onToggle: () => void; kind: "live" | "ended" }) {
  return (
    <button type="button" className={"autorun-sub-head is-" + kind} data-rail-row="" aria-expanded={open} onClick={onToggle}>
      <Icon name={open ? "chevron-down" : "chevron-right"} />
      {label}
    </button>
  );
}

function Category({
  label,
  categoryId,
  queue,
  runs,
  now,
  onDialog,
  openMenu,
  onSettings,
  activeName,
}: {
  label: string;
  /** The category's live queue settings (undefined: none are known). */
  queue: MemoQueue | undefined;
  /** "" for runs no queue category names: no settings, no history. */
  categoryId: string;
  runs: RunRow[];
  now: number;
  onDialog: (d: RunDialog) => void;
  openMenu: OpenSessionMenu;
  onSettings: (id: string) => void;
  activeName: string | null;
}) {
  const tr = useT();
  const [showLive, setShowLive] = useState(true);
  const [showEnded, setShowEnded] = useState(false);
  // Published on the section so the sub-headers pin directly under this header, wrapped or not.
  const headRef = usePublishedHeight<HTMLDivElement>("--autorun-cat-head-h");
  const live = runs.filter((r) => r.state !== "ended");
  const ended = runs.filter((r) => r.state === "ended");
  return (
    <section className="autorun-cat" data-autorun-category={label}>
      <div className="autorun-cat-head" ref={headRef}>
        <Icon name="repo" className="autorun-cat-ic" />
        <h3 className="autorun-cat-name">{label}</h3>
        {categoryId && (
          <>
            <button type="button" className="autorun-cat-act" aria-label={tr("memo.autorun_settings")} title={tr("memo.autorun_settings")} onClick={() => onSettings(categoryId)}>
              <Icon name="settings-gear" />
            </button>
            <CategoryMenu categoryId={categoryId} queue={queue} />
          </>
        )}
      </div>
      {live.length > 0 && (
        <div className="autorun-group" data-autorun-group="live">
          <SubHead kind="live" label={tr("autorun.running_head", { n: live.length })} open={showLive} onToggle={() => setShowLive((v) => !v)} />
          {showLive && (
            <ul className="autorun-list">
              {live.map((r) => (
                <RunItem key={r.root.name} run={r} now={now} onDialog={onDialog} openMenu={openMenu} activeName={activeName} />
              ))}
            </ul>
          )}
        </div>
      )}
      {ended.length > 0 && (
        <div className="autorun-group" data-autorun-group="ended">
          <SubHead kind="ended" label={tr("autorun.ended", { n: ended.length })} open={showEnded} onToggle={() => setShowEnded((v) => !v)} />
          {showEnded && (
            <ul className="autorun-list">
              {ended.map((r) => (
                <RunItem key={r.root.name} run={r} now={now} onDialog={onDialog} openMenu={openMenu} activeName={activeName} />
              ))}
            </ul>
          )}
        </div>
      )}
    </section>
  );
}

/** "Clear finished runs": archives the stopped sessions of ended runs after one confirmation, then
 *  offers the worktrees of merged runs to the delete dialog (its own confirmation). */
function ClearFinished({ runs }: { runs: RunRow[] }) {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const repos = useReposStore((s) => s.repos);
  const closeSessionPanes = useLayoutStore((s) => s.closeSessionPanes);
  const [del, setDel] = useState<RepoTreeNode[] | null>(null);
  const [busy, setBusy] = useState(false);
  const runsRef = useRef(runs);
  runsRef.current = runs;
  // Read at the moment of use: a member can start a session in a candidate copy while the
  // confirmation is open or the archive runs, and that session must not be swept along.
  const latest = () => planClearFinished(runsRef.current, useSessionsStore.getState().sessions, useReposStore.getState().repos);
  const nothing = useMemo(() => {
    const p = planClearFinished(runs, sessions, repos);
    return p.archive.length === 0 && p.nodes.length === 0;
  }, [runs, sessions, repos]);
  const click = async () => {
    if (busy) return;
    if (latest().archive.length > 0) {
      const ok = await askConfirm({
        title: tr("autorun.clear"),
        body: tr("autorun.clear_confirm", { count: latest().archive.flat().length }),
        confirmLabel: tr("autorun.clear_do"),
      });
      if (!ok) return;
      setBusy(true);
      let archived = 0;
      let failed = 0;
      try {
        for (const group of latest().archive) {
          // Deepest first: a session whose archive failed keeps its ancestors, so the run stays
          // listed with everything below it and the next clear retries.
          const keep = new Set<string>();
          for (const s of group) {
            if (keep.has(s.name)) continue;
            const res = await (agentOf(s.kind).caps.ephemeral
              ? sessionDelete(s.name, { stop: true })
              : raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" })
            ).catch(() => null);
            if (res?.ok) {
              archived++;
              closeSessionPanes(s.name);
              continue;
            }
            failed++;
            const j = await res?.json().catch(() => null);
            toast(tr("autorun.clear_failed", { name: s.name, err: j?.error ? errText(j.error) : "" }));
            for (const n of ancestorNames(s.name, useSessionsStore.getState().sessions)) keep.add(n);
          }
        }
      } finally {
        setBusy(false);
        await useSessionsStore.getState().refresh();
      }
      if (archived > 0 && failed === 0) toast(tr("autorun.cleared", { count: archived }), { kind: "success" });
    }
    // Planned again from what the list says now, after the archive settled.
    const nodes = latest().nodes;
    if (nodes.length > 0) setDel(nodes);
  };
  return (
    <>
      <IconButton icon={busy ? "loading" : "clear-all"} spin={busy} className="autorun-clear" label={tr("autorun.clear")} disabled={nothing || busy} onClick={() => void click()} />
      {del && (
        <DeleteCopyModal nodes={del} onClose={() => setDel(null)} onDeleted={(count) => toast(tr("rp.del.done", { count }), { kind: "success" })} />
      )}
    </>
  );
}

export const AutoRunSection = memo(function AutoRunSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const shown = useViewShown();
  useAutoRunFeed(shown);
  // The bar's own read may be minutes old when the view comes back.
  useEffect(() => {
    if (shown) refreshAutoRuns();
  }, [shown]);
  const { queues, tenantEnabled, deliveries } = useScopedAutoRuns();
  const reload = reloadAutoRuns;
  const wset = useActiveWorkingSet();
  const [dialog, setDialog] = useState<RunDialog | null>(null);
  // The session menu is the view's, not a row's: its confirmation and share/handoff dialogs then
  // survive the row moving between "Running" and "Ended" or leaving the list after an archive.
  // `s` is kept after the menu closes for the same reason (the menu hosts those dialogs).
  const actions = useSessionActions();
  const workspaceRunning = useWorkspaceStore((st) => st.state) === "running";
  const [menu, setMenu] = useState<{ s: Session; at: { x: number; y: number }; open: boolean } | null>(null);
  const openMenu = useCallback<OpenSessionMenu>((s, at) => {
    announceMenu("session");
    setMenu({ s, at, open: true });
  }, []);
  useCloseOnOtherMenu("session", () => setMenu((m) => (m ? { ...m, open: false } : m)));
  const menuSession = menu ? (sessions.find((x) => x.name === menu.s.name) ?? menu.s) : null;
  const [settingsFor, setSettingsFor] = useState<string | null>(null);
  const [reloading, setReloading] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(id);
  }, []);
  const allRuns = useMemo(() => buildRuns(sessions, deliveries), [sessions, deliveries]);
  const runs = useMemo(() => scopeRuns(allRuns, queues, wset), [allRuns, queues, wset]);
  // The session open in the focused pane marks its run row or child row, as the Sessions view
  // marks its own active row.
  const activeName = useLayoutStore((st) => activePane(st.layout)?.session ?? null);
  // Scrolls only, never focuses: keyboard focus stays where the member put it. A row inside a
  // closed "Ended N" fold is not rendered, so it is not found and the fold stays closed.
  // A category the queue list no longer names (its settings were deleted) is "Other runs" too.
  const keyOf = (id: string) => (queues.some((q) => q.categoryId === id) ? id : "");
  // Where the row sits: it can appear late (sessions read) or move between categories (a run no
  // delivery names yet is under "Other runs"), so a change of place scrolls again; a plain
  // re-poll leaves it unchanged and does not.
  const place = (() => {
    const r = activeName ? runs.find((x) => x.root.name === activeName || x.children.some((c) => c.name === activeName)) : undefined;
    return r ? [r.root.name, keyOf(r.categoryId), r.state === "ended" ? "ended" : "live"].join("\u0000") : "";
  })();
  useEffect(() => {
    if (!shown || !activeName || !place) return;
    const id = requestAnimationFrame(() => {
      document.querySelector<HTMLElement>(".autorun-view [aria-current='true']")?.scrollIntoView({ block: "nearest" });
    });
    return () => cancelAnimationFrame(id);
  }, [shown, activeName, place]);
  const labelOf = (id: string): string => {
    const q = queues.find((x) => x.categoryId === id);
    return q ? [q.repo, q.category].filter(Boolean).join(" / ") || tr("memo.autorun") : tr("autorun.other");
  };
  const ids = [...new Set(runs.map((r) => keyOf(r.categoryId)))];
  const active = runs.filter((r) => r.state !== "ended").length;
  const doReload = async () => {
    setReloading(true);
    reload();
    await useSessionsStore.getState().refresh();
    setReloading(false);
  };
  const settingsQueue = settingsFor ? queues.find((q) => q.categoryId === settingsFor) : undefined;
  // Rendered whether or not a run is left: deleting a run's last sessions empties the list while
  // the dialog is still showing its outcome.
  const dialogs = (
    <>
      {dialog?.kind === "stop" && (
        <StopSessionsModal
          node={dialog.node}
          only={dialog.only}
          onClose={() => setDialog(null)}
          onDone={(now, after) => {
            const parts = [
              ...(now > 0 ? [tr("rp.stop.done_now", { count: now })] : []),
              ...(after > 0 ? [tr("rp.stop.done_after", { count: after })] : []),
            ];
            toast(parts.join(tr("common.list_sep")), { kind: "success" });
          }}
        />
      )}
      {dialog?.kind === "delete" && (
        <DeleteCopyModal node={dialog.node} onClose={() => setDialog(null)} onDeleted={(count) => toast(tr("rp.del.done", { count }), { kind: "success" })} />
      )}
      {settingsQueue && (
        <MemoQueueSettingsModal
          key={settingsQueue.categoryId}
          categoryId={settingsQueue.categoryId}
          categoryName={settingsQueue.category}
          queue={settingsQueue}
          tenantEnabled={tenantEnabled}
          destinations={queues
            .filter((q) => q.categoryId !== settingsQueue.categoryId)
            .map((q) => ({ id: q.categoryId, label: q.repo ? q.repo + " / " + q.category : q.category }))}
          onClose={() => setSettingsFor(null)}
          // The save already wrote the shared cache; the refetches only reconcile with the server.
          onChanged={() => {
            reload();
            useMemoStore.getState().bump();
          }}
        />
      )}
    </>
  );
  return (
    <Section
      id="autorun"
      title={tr("memo.autorun")}
      icon="play"
      count={active}
      actions={
        <>
          <ClearFinished runs={runs} />
          <IconButton icon={reloading ? "loading" : "refresh"} spin={reloading} label={tr("autorun.reload")} disabled={reloading} onClick={() => void doReload()} />
        </>
      }
    >
      <div className="autorun-view">
        {runs.length === 0 && <div className="pane-empty">{tr("autorun.none")}</div>}
        {ids.map((id) => (
          <Category
            key={id}
            label={labelOf(id)}
            categoryId={id}
            queue={queues.find((q) => q.categoryId === id)}
            runs={runs.filter((r) => keyOf(r.categoryId) === id)}
            now={now}
            onDialog={setDialog}
            openMenu={openMenu}
            onSettings={setSettingsFor}
            activeName={activeName}
          />
        ))}
      </div>
      {dialogs}
      {menu && menuSession && (
        <SessionMenu
          s={menuSession}
          actions={actions}
          running={workspaceRunning}
          open={menu.open}
          place={(el) => placeFixed(el, menu.at.x, menu.at.y)}
          onClose={() => setMenu((m) => (m ? { ...m, open: false } : m))}
        />
      )}
    </Section>
  );
});
