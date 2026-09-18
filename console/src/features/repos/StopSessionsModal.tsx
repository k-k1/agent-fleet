// StopSessionsModal — "stop every session under this working copy", from the rail row the
// user right-clicked.
//
// It is deleteTree's dialog seen from the other side: the same subtree, the same "tick a row
// to include it" layout, but a reversible action. Stopping keeps the conversation and the
// session resumes, so the question this dialog asks is not "what is lost" but "what is still
// running" — and the answer is per row, because a session mid-turn and a session parked on a
// question are stopped by different means (stopTree.ts).
//
// Two rules the layout serves:
//   - A row's badge says what WILL be sent to it ("stop now" / "after the turn"), not how
//     risky it is. The mode is the whole substance of the choice here, and it changes under
//     the force tick, so it has to be readable per row at the moment it changes.
//   - The rows af cannot speak for start unticked: a shell, whose running command it cannot
//     see, and a session the user pinned awake on purpose. Ticking one is the user saying it.
import { useMemo, useState } from "react";
import type { CSSProperties } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { raw, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { displayName, stateInfo } from "../../lib/sessionview.ts";
import { kindIcon } from "../../lib/sessionkind.ts";
import type { RepoTreeNode } from "../../lib/project.ts";
import {
  planStopTree,
  defaultStopSelection,
  stopOrder,
  stopModeOf,
  summarizeStop,
  type StopRow,
} from "./stopTree.ts";

const enc = encodeURIComponent;

/** Per-row outcome once the run has touched it. */
interface RowResult {
  ok: boolean;
  /** Already-localized line to show under the row. */
  note: string;
}

interface StopSessionsModalProps {
  /** The right-clicked copy and the subtree the rail nests under it. */
  node: RepoTreeNode;
  onClose: () => void;
  /** Fired only when every ticked row was accepted and the dialog closes itself — the one
   *  moment the outcome is no longer readable on screen. A partial run stays open with a
   *  note per row instead. */
  onDone?: (now: number, after: number) => void;
}

export function StopSessionsModal({ node, onClose, onDone }: StopSessionsModalProps) {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  // Frozen at open, as the delete plan is: the sessions list is polled every 4s, and a row
  // appearing or changing state under the pointer is how a tick lands on a row nobody read.
  const groups = useMemo(() => planStopTree(node, sessions), [node]); // eslint-disable-line react-hooks/exhaustive-deps
  const [selected, setSelected] = useState<Set<string>>(() => defaultStopSelection(groups));
  const [force, setForce] = useState(false);
  const [busy, setBusy] = useState(false);
  const [results, setResults] = useState<Map<string, RowResult>>(new Map());
  const sum = summarizeStop(groups, selected, force);
  const done = results.size > 0;

  const toggle = (name: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });

  /** Sends one row and returns its result line. `after` arms the stop-after-turn flag
   *  (docs/log/85); `now` is the halt the session menu's stop button takes. */
  const send = async (r: StopRow): Promise<RowResult> => {
    const name = r.session.name;
    const res =
      stopModeOf(r, force) === "after"
        ? await raw(`api/sessions/${enc(name)}/stop-after-turn`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ on: true }),
          }).catch(() => null)
        : await raw(`api/sessions/${enc(name)}/halt`, { method: "POST" }).catch(() => null);
    if (!res?.ok) {
      const j = await res?.json().catch(() => null);
      return { ok: false, note: tr("rp.stop.row_failed", { err: j?.error ? errText(j.error) : "" }) };
    }
    return {
      ok: true,
      note: stopModeOf(r, force) === "after" ? tr("rp.stop.row_armed") : tr("rp.stop.row_done"),
    };
  };

  const run = async () => {
    const order = stopOrder(groups, selected);
    if (order.length === 0) return;
    setBusy(true);
    const out = new Map<string, RowResult>();
    let now = 0;
    let after = 0;
    try {
      // Sequential: a halt is a tmux kill through the one Agent, and a failure has to be
      // readable against the row that produced it rather than lost in a batch.
      for (const r of order) {
        const res = await send(r);
        out.set(r.session.name, res);
        if (!res.ok) continue;
        if (stopModeOf(r, force) === "after") after += 1;
        else now += 1;
      }
    } finally {
      setBusy(false);
      setResults(out);
    }
    const refresh = () => void useSessionsStore.getState().refresh();
    refresh();
    // A halted pane takes a moment to read back as stopped — the same second pass the
    // session menu's stop makes, or the rows sit there still looking alive.
    setTimeout(refresh, 1200);
    if (now + after === order.length) {
      onDone?.(now, after);
      onClose();
    }
  };

  return (
    <Modal title={tr("rp.stop_sessions_title")} onClose={onClose} lockClose={busy} className="wcstop-modal">
      <div className="ui-modal-body">
        <p className="wcstop-intro">
          {groups.length > 1
            ? tr("rp.stop.intro_tree", { name: node.repo.name, count: groups.length - 1 })
            : tr("rp.stop.intro", { name: node.repo.name })}
        </p>

        <ul className="wcstop-list">
          {groups.map((g) => (
            <li
              key={g.repo.name}
              className="wcstop-group"
              style={{ "--wcstop-depth": Math.min(g.depth, 3) } as CSSProperties}
            >
              <p className="wcstop-copy">
                <Icon name={g.repo.worktree ? "repo-forked" : "repo"} />
                <span className="wcstop-copy-name">{g.repo.name}</span>
                {g.repo.branch && <span className="wcstop-branch">{g.repo.branch}</span>}
              </p>
              <ul className="wcstop-rows">
                {g.rows.map((r) => {
                  const name = r.session.name;
                  const st = stateInfo(r.session);
                  const res = results.get(name);
                  const picked = selected.has(name);
                  const mode = stopModeOf(r, force);
                  return (
                    <li key={name} className="wcstop-row">
                      <label className="wcstop-head">
                        <input
                          type="checkbox"
                          checked={picked}
                          disabled={busy || done}
                          onChange={() => toggle(name)}
                        />
                        <Icon name={kindIcon(r.session.kind)} className="wcstop-kind" />
                        <span className="wcstop-name">{displayName(r.session)}</span>
                        <span className={"session-state " + st.cls} title={st.text}>
                          <Icon name={st.icon} spin={st.spin} /> {st.short ?? st.text}
                        </span>
                        {/* Only a ticked row names an action: an unticked one has none, and a
                            badge on it would read as a promise. */}
                        {picked && (
                          <span
                            className={
                              "wcstop-mode wcstop-mode-" + mode + (r.busy && mode === "now" ? " is-cut" : "")
                            }
                          >
                            {tr(mode === "after" ? "rp.stop.mode_after" : "rp.stop.mode_now")}
                          </span>
                        )}
                      </label>
                      {r.whyKey && !res && (
                        <p className="wcstop-why">{tr(r.whyKey, { left: r.pinned })}</p>
                      )}
                      {res && (
                        <p className={"wcstop-result" + (res.ok ? "" : " is-failed")}>
                          <Icon name={res.ok ? "check" : "error"} />
                          {res.note}
                        </p>
                      )}
                    </li>
                  );
                })}
              </ul>
            </li>
          ))}
        </ul>

        {/* Offered whenever a ticked row is mid-turn: the tick is what turns the arm (which
            loses nothing) into a halt (which cuts the reply being written off). */}
        {sum.busy > 0 && (
          <label className="wcstop-opt">
            <input
              type="checkbox"
              checked={force}
              disabled={busy || done}
              onChange={(e) => setForce(e.target.checked)}
            />
            <span>{tr("rp.stop.force", { count: sum.busy })}</span>
            <span className="wcstop-opt-hint is-warn">{tr("rp.stop.force_hint")}</span>
          </label>
        )}
      </div>

      <footer className="ui-modal-foot wcstop-foot">
        <span className="wcstop-summary">
          {tr("rp.stop.summary", { count: sum.total })}
          {sum.after > 0 && tr("rp.stop.summary_after", { count: sum.after })}
          {sum.cut > 0 && <span className="wcstop-warn">{tr("rp.stop.force_warn", { count: sum.cut })}</span>}
        </span>
        <span className="wcstop-foot-actions">
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {done ? tr("common.close") : tr("common.cancel")}
          </Button>
          {!done && (
            <Button variant="primary" onClick={() => void run()} disabled={busy || sum.total === 0}>
              {tr("rp.stop.run", { count: sum.total })}
            </Button>
          )}
        </span>
      </footer>
    </Modal>
  );
}
