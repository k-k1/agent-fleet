// The Auto-run view (ADR 0140, note of 2026-11-21): one compact row per run of a memo auto-run
// queue, grouped by the queue's category. Unattended work is listed here and not in the
// repository tree. A row opens the run's session; the sessions the run started (reviewers) sit
// one level under it, and finished runs fold into "Ended N".
import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "../../ui/Icon.tsx";
import { Section } from "../../ui/Section.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { usePublishedHeight } from "../../lib/usePublishedHeight.ts";
import { useActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { useLeftRail } from "../../core/store/leftRail.ts";
import { useMemoStore } from "../memo/store.ts";
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
import { useReposStore } from "../repos/store.ts";
import { StopSessionsModal } from "../repos/StopSessionsModal.tsx";
import { DeleteCopyModal } from "../repos/DeleteCopyModal.tsx";
import type { RepoTreeNode } from "../../lib/project.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat } from "../sessions/open.ts";
import { refreshAutoRuns, reloadAutoRuns, useAutoRunFeed, pendingReveal, useAutoRunReveal, useScopedAutoRuns } from "./store.ts";
import { buildRuns, fmtElapsed, scopeRuns, waitsForMember, type RunRow } from "./runs.ts";
import { ancestorNames, planClearFinished, runStopScope, runWorktreeNode } from "./runActions.ts";
import "./autorun.css";

const STATE_KEY = { running: "autorun.state_running", waiting: "autorun.state_waiting", ended: "autorun.state_ended" } as const;

const PR_KEY = { open: "autorun.pr_open", merged: "autorun.pr_merged", closed: "autorun.pr_closed" } as const;

/** What an open dialog acts on, frozen when the menu item is chosen. The view owns the dialog
 *  (not the row) so it survives the row moving to "Ended" or the list refreshing under it. */
type RunDialog = { kind: "stop"; node: RepoTreeNode; only: Set<string> } | { kind: "delete"; node: RepoTreeNode };

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
  useMenuRoving(menuRef, !!at);
  useLayoutEffect(() => {
    if (at && menuRef.current) placeFixed(menuRef.current, at.x, at.y);
  });
  const open = () => {
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

function RunItem({ run, now, onDialog }: { run: RunRow; now: number; onDialog: (d: RunDialog) => void }) {
  const tr = useT();
  const { root, item, pr, children } = run;
  const end = run.state === "ended" ? run.endedAt : now;
  const elapsed = Number.isFinite(run.startedAt) && Number.isFinite(end) ? fmtElapsed(end - run.startedAt) : "";
  const title = item ? `${item.label} ${item.title || displayName(root)}`.trim() : displayName(root);
  return (
    <li className={"autorun-run is-" + run.state} data-autorun-run={root.name} data-autorun-state={run.state}>
      <button
        type="button"
        className="autorun-run-main"
        data-rail-row=""
        onClick={() => openSessionChat(root.name)}
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
              <button type="button" className="autorun-kid" data-rail-row="" onClick={() => openSessionChat(c.name)} title={tr("autorun.open", { name: c.name })}>
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

/** The "..." menu of a category head: the run history and the category's place in the Memo queue
 *  view, both of which that view already owns. */
function CategoryMenu({ categoryId }: { categoryId: string }) {
  const tr = useT();
  const [at, setAt] = useState<{ x: number; y: number } | null>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  useDismiss([btnRef, menuRef], !!at, () => setAt(null));
  useMenuRoving(menuRef, !!at);
  useLayoutEffect(() => {
    if (at && menuRef.current) placeFixed(menuRef.current, at.x, at.y);
  });
  const reveal = (history: boolean) => {
    setAt(null);
    useLeftRail.getState().showView("memo");
    useMemoStore.getState().revealCategory(categoryId, history);
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
  runs,
  now,
  onDialog,
  onSettings,
}: {
  label: string;
  /** "" for runs no queue category names: no settings, no history. */
  categoryId: string;
  runs: RunRow[];
  now: number;
  onDialog: (d: RunDialog) => void;
  onSettings: (id: string) => void;
}) {
  const tr = useT();
  const [showLive, setShowLive] = useState(true);
  const [showEnded, setShowEnded] = useState(false);
  // Published on the section so the sub-headers pin directly under this header, wrapped or not.
  const headRef = usePublishedHeight<HTMLDivElement>("--autorun-cat-head-h");
  const live = runs.filter((r) => r.state !== "ended");
  const ended = runs.filter((r) => r.state === "ended");
  // A link from the memo run history opens the fold the run sits in, also when the run only
  // reaches this category (or moves between its folds) once its delivery has been read.
  const revealN = useAutoRunReveal((s) => s.n);
  const names = live.map((r) => r.root.name).join("\u0000") + "\u0001" + ended.map((r) => r.root.name).join("\u0000");
  useEffect(() => {
    const name = pendingReveal();
    if (!name) return;
    if (live.some((r) => r.root.name === name)) setShowLive(true);
    if (ended.some((r) => r.root.name === name)) setShowEnded(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [revealN, names]);
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
            <CategoryMenu categoryId={categoryId} />
          </>
        )}
      </div>
      {live.length > 0 && (
        <div className="autorun-group" data-autorun-group="live">
          <SubHead kind="live" label={tr("autorun.running_head", { n: live.length })} open={showLive} onToggle={() => setShowLive((v) => !v)} />
          {showLive && (
            <ul className="autorun-list">
              {live.map((r) => (
                <RunItem key={r.root.name} run={r} now={now} onDialog={onDialog} />
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
                <RunItem key={r.root.name} run={r} now={now} onDialog={onDialog} />
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
  const [del, setDel] = useState<ReturnType<typeof planClearFinished>["node"]>(null);
  const [busy, setBusy] = useState(false);
  const runsRef = useRef(runs);
  runsRef.current = runs;
  // Read at the moment of use: a member can start a session in a candidate copy while the
  // confirmation is open or the archive runs, and that session must not be swept along.
  const latest = () => planClearFinished(runsRef.current, useSessionsStore.getState().sessions, useReposStore.getState().repos);
  const nothing = useMemo(() => {
    const p = planClearFinished(runs, sessions, repos);
    return p.archive.length === 0 && !p.node;
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
    const node = latest().node;
    if (node) setDel(node);
  };
  return (
    <>
      <IconButton icon={busy ? "loading" : "clear-all"} spin={busy} className="autorun-clear" label={tr("autorun.clear")} disabled={nothing || busy} onClick={() => void click()} />
      {del && (
        <DeleteCopyModal node={del} onClose={() => setDel(null)} onDeleted={(count) => toast(tr("rp.del.done", { count }), { kind: "success" })} />
      )}
    </>
  );
}

/** Scrolls the revealed run's row into view and focuses it, once it is rendered where it belongs:
 *  a run no delivery names yet is in "Other runs" and moves when its delivery arrives, so the
 *  request stays open until then (and asks for that read). */
function useRevealRun(runs: RunRow[]): void {
  const n = useAutoRunReveal((s) => s.n);
  // One explicit read per request (it invalidates what is in flight, which may predate the run); the
  // poll goes on at its own pace after that.
  const asked = useRef(0);
  useEffect(() => {
    const name = pendingReveal();
    const run = runs.find((r) => r.root.name === name);
    if (!run) return;
    if (run.categoryId === "") {
      if (asked.current !== n) {
        asked.current = n;
        reloadAutoRuns();
      }
      return;
    }
    // The view and a fold open in this commit or the next: wait a frame for the row.
    const id = requestAnimationFrame(() => {
      const row = document.querySelector<HTMLElement>(`[data-autorun-run="${CSS.escape(name)}"]`);
      if (!row) return;
      row.scrollIntoView({ block: "nearest" });
      row.querySelector<HTMLElement>(".autorun-run-main")?.focus();
      if (useAutoRunReveal.getState().n === n) useAutoRunReveal.getState().consume();
    });
    return () => cancelAnimationFrame(id);
  }, [n, runs]);
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
  const [settingsFor, setSettingsFor] = useState<string | null>(null);
  const [reloading, setReloading] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(id);
  }, []);
  const allRuns = useMemo(() => buildRuns(sessions, deliveries), [sessions, deliveries]);
  const runs = useMemo(() => scopeRuns(allRuns, queues, wset), [allRuns, queues, wset]);
  useRevealRun(runs);
  const labelOf = (id: string): string => {
    const q = queues.find((x) => x.categoryId === id);
    return q ? [q.repo, q.category].filter(Boolean).join(" / ") || tr("memo.autorun") : tr("autorun.other");
  };
  // A category the queue list no longer names (its settings were deleted) is "Other runs" too.
  const keyOf = (id: string) => (queues.some((q) => q.categoryId === id) ? id : "");
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
          // The Memo queue view keeps its own copy of the settings: bump it too, or a form opened
          // there next starts from the old values and a later save overwrites this change.
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
            runs={runs.filter((r) => keyOf(r.categoryId) === id)}
            now={now}
            onDialog={setDialog}
            onSettings={setSettingsFor}
          />
        ))}
      </div>
      {dialogs}
    </Section>
  );
});
