// The Auto-run view (ADR 0140, note of 2026-11-21): one compact row per run of a memo auto-run
// queue, grouped by the queue's category. Unattended work is listed here and not in the
// repository tree. A row opens the run's session; the sessions the run started (reviewers) sit
// one level under it, and finished runs fold into "Ended N".
import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
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
import { useTenantStore } from "../../core/store/tenant.ts";
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
import { memoQueueDeliveries, memoQueueList } from "../memo/api.ts";
import { readDeliveries, readMemoQueues } from "../memo/queues.ts";
import type { MemoQueue } from "../../types/memo.ts";
import { buildRuns, fmtElapsed, waitsForMember, type RunDelivery, type RunRow } from "./runs.ts";
import { ancestorNames, planClearFinished, runStopScope, runWorktreeNode } from "./runActions.ts";
import "./autorun.css";

const POLL_MS = 30000;

/** Queues and their deliveries, read while the view is on screen and re-read when it comes back
 *  or the tenant or the signed-in account changes. Each category is read on its own, one read at
 *  a time, so a slow one holds nobody up, and an answer to a read the scope has outgrown is
 *  dropped. The API returns only the newest fifty deliveries, so a category read keeps what an
 *  earlier one learned about a run's category, but a run that fell off the list loses its
 *  state (the sessions' own word is used for it). */
function useQueueRuns(): { queues: MemoQueue[]; tenantEnabled: boolean; deliveries: Map<string, RunDelivery>; reload: () => void } {
  const shown = useViewShown();
  const tenant = useTenantStore((s) => s.tenant);
  const identity = useTenantStore((s) => s.identityRev);
  const scopeKey = tenant + "\u0000" + identity;
  const [queues, setQueues] = useState<MemoQueue[]>([]);
  const [tenantEnabled, setTenantEnabled] = useState(false);
  // Bumped by "reload" and by a saved setting: the effect below starts over and reads at once.
  const [rev, setRev] = useState(0);
  const [deliveries, setDeliveries] = useState<Map<string, RunDelivery>>(() => new Map());
  const scope = useRef(scopeKey);
  useEffect(() => {
    if (scope.current === scopeKey) return;
    scope.current = scopeKey;
    setQueues([]);
    setTenantEnabled(false);
    setDeliveries(new Map());
  }, [scopeKey]);
  useEffect(() => {
    if (!shown) return;
    let live = true;
    let listing = false;
    const reading = new Set<string>();
    let current = new Set<string>(); // categories the latest queue list names
    // Bumped when a category leaves the list, so a read begun before cannot land after it returns.
    const epoch = new Map<string, number>();
    const readCategory = async (q: MemoQueue) => {
      if (reading.has(q.categoryId)) return;
      reading.add(q.categoryId);
      const mine = epoch.get(q.categoryId) ?? 0;
      try {
        const list = readDeliveries(await memoQueueDeliveries(q.categoryId));
        if (!live || !list || !current.has(q.categoryId) || (epoch.get(q.categoryId) ?? 0) !== mine) return;
        setDeliveries((prev) => {
          const next = new Map(prev);
          const seen = new Set(list.map((d) => d.session));
          // A run that fell off the newest-fifty list keeps its category and, once closed, its end;
          // an unsettled state is dropped (the sessions' own word is used for it).
          for (const [name, d] of prev) if (d.categoryId === q.categoryId && !seen.has(name) && d.state !== "closed") next.set(name, { ...d, state: "" });
          for (const d of list) {
            if (d.session) next.set(d.session, { categoryId: q.categoryId, state: d.state, sentAt: d.sentAt, createdAt: d.createdAt, closedAt: d.closedAt, heldBy: d.heldBy });
          }
          return next;
        });
      } catch {
        /* keep what this category had */
      } finally {
        if ((epoch.get(q.categoryId) ?? 0) === mine) reading.delete(q.categoryId);
      }
    };
    const load = async () => {
      if (listing) return;
      listing = true;
      try {
        const wire = readMemoQueues(await memoQueueList());
        if (!live || !wire) return;
        const now = new Set(wire.queues.map((q) => q.categoryId));
        for (const id of current) {
          if (now.has(id)) continue;
          epoch.set(id, (epoch.get(id) ?? 0) + 1);
          reading.delete(id);
        }
        current = now;
        setQueues(wire.queues);
        setTenantEnabled(wire.tenantEnabled);
        // A category that is gone is no longer read: whatever it left unsettled is dropped too.
        setDeliveries((prev) => {
          let next: Map<string, RunDelivery> | null = null;
          for (const [name, d] of prev) {
            if (current.has(d.categoryId) || d.state === "closed" || d.state === "") continue;
            next ??= new Map(prev);
            next.set(name, { ...d, state: "" });
          }
          return next ?? prev;
        });
        for (const q of wire.queues) void readCategory(q);
      } catch {
        /* keep the last read */
      } finally {
        listing = false;
      }
    };
    void load();
    const id = setInterval(() => {
      if (!document.hidden) void load();
    }, POLL_MS);
    return () => {
      live = false;
      clearInterval(id);
    };
  }, [shown, scopeKey, rev]);
  const reload = useCallback(() => setRev((n) => n + 1), []);
  return { queues, tenantEnabled, deliveries, reload };
}

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

export const AutoRunSection = memo(function AutoRunSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const { queues, tenantEnabled, deliveries, reload } = useQueueRuns();
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
  // A category is in the working set when its repository is; a category without a repository, and
  // the runs no queue category names, are in every set.
  const inScope = (id: string): boolean => {
    if (!wset) return true;
    const q = queues.find((x) => x.categoryId === id);
    return !q || !q.repo || wset.repos.includes(q.repo);
  };
  const runs = useMemo(
    () => (wset ? allRuns.filter((r) => inScope(r.categoryId)) : allRuns),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [allRuns, wset, queues],
  );
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
