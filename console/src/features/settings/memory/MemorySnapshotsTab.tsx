// MemorySnapshotsTab — Settings > Memory snapshots: version control for the native memory the
// agents accumulate (claude's auto-memory, codex's memories; docs/log/39 P2 / ADR 0022, adopted
// default #3). Agent Fleet's own shared memory (ADR 0108) is the neighbouring entry, MemoryTab.
//
// The persistent memory agents accumulate sits in storage that never goes away, yet it has no
// history. This tab is the operating surface for that history, its diffs and its rollbacks; the
// substance (a bare git repo) lives on the Agent side and this file only calls REST.
//
// A status header (last snapshot, automatic on/off, Snapshot now) sits over three views:
// History (the snapshot list on the left, the selected row's diff on the right, then the restore
// panel: pick a scope, then a confirm dialog; rows left by Agent Fleet's own memory writes are
// hidden unless asked, since Agent memory > Changes lists them), Targets (root overview, codex memories toggle,
// automatic capture) and Export & import (P3: bundle = full history, tar.gz = latest only; import
// replaces the selected scope). The diff uses the same <Diff> as the SCM commit pane, so there is
// one way a diff looks rather than two.
//
// A rollback never rewrites history: a pre-restore snapshot is taken automatically before it
// applies (docs/log/39 ★2). Every action on this screen is therefore undoable, and the
// confirm-dialog wording is written on that assumption.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, errDetail, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { Diff } from "../../scm/GitDiff.tsx";
import { OnOff, Row } from "../parts/controls.tsx";
import { useT, tMaybe } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { humanSize } from "../../../lib/filemeta.ts";
import type { RootsPayload, Snapshot } from "./memoryTypes.ts";
import type { RestoreBody } from "./memoryRestore.tsx";
import type { RestoreScopeState } from "./memoryRestore.tsx";
import { RestorePanel } from "./memoryRestore.tsx";
import { TransferSection } from "./memoryTransfer.tsx";
import { ViewNav, useSectionView, type ViewDef } from "./viewNav.tsx";
import { useSettings } from "../../../lib/settings.ts";
import { useSettingsUI, lastSnapshotsShowAf, rememberSnapshotsShowAf } from "../store.ts";
import { kindColorClasses } from "../../../lib/sessionkind.ts";

// Trigger label. The trailer value the Agent returns (auto/manual/pre-restore/restore/import/agent-memory)
// is used as the key directly and an unknown value is printed raw, so adding a new trigger
// upstream does not break this screen.
const triggerLabel = (trigger: string): string =>
  tMaybe("mem.trigger_" + trigger.replace(/-/g, "_")) ?? trigger ?? "";

// Why a root is inactive. Same rule as the trigger label: an unknown reason is printed raw,
// so a new reason from the Agent does not break the screen.
const reasonLabel = (reason: string): string => tMaybe("mem.reason_" + reason) ?? reason ?? "";

// The trigger of the snapshot an Agent Fleet memory write leaves. Agent memory > Changes already
// lists those writes, so History hides them unless asked.
const AF_TRIGGER = "agent-memory";

export const SNAPSHOTS_SECTION = "memorysnapshots";
export const SNAPSHOT_VIEWS = ["history", "targets", "transfer"] as const;
const VIEWS: readonly string[] = SNAPSHOT_VIEWS;

export function MemorySnapshotsTab() {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";

  const [data, setData] = useState<RootsPayload | null>(null);
  const [snaps, setSnaps] = useState<Snapshot[] | null>(null);
  const [err, setErr] = useState("");
  // The snapshot list failing is not an empty history: it gets its own message and a retry.
  const [snapErr, setSnapErr] = useState("");
  // Mirror of snapErr for a jump already in flight, whose closure still holds the old value.
  const snapErrRef = useRef("");
  const [reload, setReload] = useState(0);
  const [busy, setBusy] = useState(false);

  // The selected snapshot (a history row) and its diff.
  const [sel, setSel] = useState("");
  const [diff, setDiff] = useState<string | null>(null);
  // Jump-to-timestamp (the datetime-local value).
  const [at, setAt] = useState("");
  // The restore panel. null = closed.
  const [scope, setScope] = useState<RestoreScopeState | null>(null);
  const [view, setView] = useSectionView(SNAPSHOTS_SECTION, VIEWS, "history");
  const [showAf, setShowAf] = useState(lastSnapshotsShowAf);
  const openSettings = useSettingsUI((s) => s.openSettings);
  const agentMemoryOn = useSettings().agentMemory;

  const load = useCallback(
    async (signal: AbortSignal) => {
      if (!running) return true; // don't call while stopped; deps re-run this once it starts
      const [r, s] = await Promise.all([
        api("api/agents/memory/roots"),
        api("api/agents/memory/snapshots?limit=100"),
      ]);
      if (signal.aborted) return true;
      if (isTransientErr(r) || isTransientErr(s)) return false;
      if (r?.error) {
        // A failed roots request is a failed History too: leaving `snaps` null would read "Loading…" for good.
        const msg = errText(r.error);
        setErr(msg);
        snapErrRef.current = msg;
        setSnapErr(msg);
        setSnaps([]);
        return true;
      }
      setErr("");
      setData(r);
      snapErrRef.current = s?.error ? errText(s.error) : "";
      setSnapErr(snapErrRef.current);
      setSnaps(s?.error ? [] : (s?.snapshots ?? []));
      return true;
    },
    [running],
  );
  useRetryLoad(load, [running, reload]);

  // Follow the selection when the history is replaced, so a vanished rev is never held on to.
  useEffect(() => {
    if (!snaps?.length) {
      setSel("");
      return;
    }
    // Fall back to the newest row that is listed; only when every row is hidden take a hidden one,
    // so the pane never sits empty over a non-empty history.
    setSel((cur) =>
      cur && snaps.some((s) => s.rev === cur)
        ? cur
        : (snaps.find((s) => showAf || s.trigger !== AF_TRIGGER) ?? snaps[0]).rev,
    );
  }, [snaps, showAf]);

  // The selected row's diff (with no "from", the change that snapshot introduced).
  useEffect(() => {
    if (!sel) {
      setDiff(null);
      return;
    }
    let live = true;
    setDiff(null);
    api("api/agents/memory/diff?to=" + encodeURIComponent(sel))
      .then((d) => {
        if (!live) return;
        setDiff(d?.error ? "" : (d.diff ?? ""));
      })
      .catch(() => live && setDiff(""));
    return () => {
      live = false;
    };
  }, [sel]);

  const setAuto = async (on: boolean) => {
    const res = await apiJSON("api/agents/memory/settings", "PUT", {
      auto: on,
    });
    if (res?.error) {
      toast(tr("common.save_failed"));
      return;
    }
    setData((d) => (d ? { ...d, auto: !!res.auto, autoLocked: !!res.autoLocked } : d));
  };

  // Fleet-wide enablement of codex memories (docs/log/39 P4, resolution #4). The setting
  // really lives in codex's own config.toml, so the write goes through the existing
  // /codex/settings. Enabling it does not create ~/.codex/memories until codex next runs, so
  // right afterwards the root reads as enabled but not yet created (roots is re-read to take
  // the state from the Agent rather than assume it).
  const setCodexMemories = async (on: boolean) => {
    const res = await apiJSON("api/codex/settings", "PUT", { memories: on });
    if (res?.error) {
      toast(errText(res.error));
      return;
    }
    toast(on ? tr("mem.codex_enabled") : tr("mem.codex_disabled_toast"));
    setReload((n) => n + 1);
  };

  const snapshotNow = async () => {
    setBusy(true);
    try {
      const res = await apiJSON("api/agents/memory/snapshots", "POST", {
        trigger: "manual",
      });
      if (res?.error) {
        toast(errDetail(res.error));
        return;
      }
      toast(res.committed ? tr("mem.snapshot_taken") : tr("mem.snapshot_unchanged"));
      setReload((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  // Jump to a timestamp: select the newest snapshot at or before it (the rollback semantics).
  const jumpTo = async () => {
    if (!at) return;
    const iso = new Date(at).toISOString();
    const res = await api("api/agents/memory/snapshots?limit=1&before=" + encodeURIComponent(iso));
    const hit = res?.snapshots?.[0];
    // The list failed to reload while this jump was out: selecting now would show a row beside the error.
    if (snapErrRef.current) return;
    if (!hit) {
      toast(tr("mem.jump_none"));
      return;
    }
    // The history is newest-first, so insert while keeping `at` descending rather than
    // prepending — otherwise an old snapshot from beyond the limit lands at the top and the
    // chronology breaks.
    if (!snaps?.some((s) => s.rev === hit.rev))
      setSnaps((cur) => [...(cur ?? []), hit].sort((a, b) => (a.at < b.at ? 1 : a.at > b.at ? -1 : 0)));
    setSel(hit.rev);
  };

  const runRestore = async (rev: string, body: RestoreBody, label: string) => {
    const anyBusy = (data?.roots ?? []).some((r) => r.busy);
    const ok = await askConfirm({
      title: tr("mem.restore_confirm_title"),
      body: (
        <>
          <p>{tr("mem.restore_confirm_body", { scope: label })}</p>
          <p className="muted">{tr("mem.restore_undo_hint")}</p>
          {anyBusy && <p className="mem-warn">{tr("mem.restore_busy_warn")}</p>}
        </>
      ),
      confirmLabel: tr("mem.restore_do"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      // rev also goes in the query: the CP audit ledger takes its target from the URL only
      // and never reads the body. The body is what actually drives the operation.
      const res = await apiJSON("api/agents/memory/restore?rev=" + encodeURIComponent(rev), "POST", body);
      if (res?.error) {
        toast(errDetail(res.error));
        return;
      }
      toast(
        res.committed
          ? tr("mem.restored", {
              written: res.written?.length ?? 0,
              deleted: res.deleted?.length ?? 0,
            })
          : tr("mem.restore_nochange"),
        { kind: "success", persist: true },
      );
      setScope(null);
      setReload((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  if (!running) {
    return (
      <div className="mem-tab">
        <EmptyState icon="archive" title={tr("mem.ws_required_title")} hint={tr("mem.ws_required_hint")}>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("mem.start_ws")}
          </Button>
        </EmptyState>
      </div>
    );
  }

  const selected = snaps?.find((s) => s.rev === sel) ?? null;
  // The selected row is always listed, hidden or not: a jump, or the toggle turned off while a
  // hidden row is open, must not take away the row whose diff and Restore this point are on screen.
  const listed = (s: Snapshot) => showAf || s.trigger !== AF_TRIGGER || s.rev === sel;
  // The rows in order, each run of hidden ones folded into one count.
  const rows: ({ snap: Snapshot } | { hidden: number; key: string })[] = [];
  for (const s of snaps ?? []) {
    if (listed(s)) rows.push({ snap: s });
    else {
      const last = rows[rows.length - 1];
      if (last && "hidden" in last) last.hidden++;
      else rows.push({ hidden: 1, key: "hidden-" + s.rev });
    }
  }
  const toggleAf = (on: boolean) => {
    setShowAf(on);
    rememberSnapshotsShowAf(on);
  };

  const defs: ViewDef[] = [
    { key: "history", label: tr("mem.view_history") },
    { key: "targets", label: tr("mem.view_targets") },
    { key: "transfer", label: tr("mem.view_transfer") },
  ];

  return (
    <div className="mem-tab">
      <p className="muted ds-note">{tr("mem.intro")}</p>
      {err && <p className="mem-warn">{err}</p>}

      {/* Status header: the three things worth knowing from any view */}
      <div className="mem-status">
        <div className="mem-status-line">
          {/* Nothing is asserted until the roots have been read: an unread or failed load is not "off" or "never". */}
          {data ? (
            <>
              <span className="muted">
                {data.lastSnapshot
                  ? tr("mem.last_snapshot", { when: fmtDateTime(data.lastSnapshot, DATETIME_FULL) })
                  : tr("mem.never")}
              </span>
              <span className={"mem-badge " + (data.auto ? "mem-on" : "mem-off")}>
                {tr(data.auto ? "mem.status_auto_on" : "mem.status_auto_off")}
              </span>
            </>
          ) : (
            !err && <span className="muted">{tr("common.loading")}</span>
          )}
          <button type="button" disabled={busy} onClick={snapshotNow}>
            {tr("mem.snapshot_now")}
          </button>
        </div>
      </div>
      <ViewNav label={tr("mem.views_label")} views={defs} value={view} onChange={setView} prefix="mem-snap" />
      <div role="tabpanel" id="mem-snap-panel" aria-labelledby={`mem-snap-tab-${view}`} className="mem-view">
        {/* Targets: roots, codex toggle, capture triggers */}
        {view === "targets" && (
          <section className="mem-section">
            {!data ? (
              err ? (
                <p className="mem-warn pad">
                  <button type="button" className="linklike" onClick={() => setReload((n) => n + 1)}>
                    {tr("mem.history_retry")}
                  </button>
                </p>
              ) : (
                <p className="muted pad">{tr("common.loading")}</p>
              )
            ) : data.roots.length === 0 && !data.inactive?.length ? (
              <p className="muted pad">{tr("mem.no_roots")}</p>
            ) : (
              <ul className="mem-roots">
                {data.roots.map((r) => (
                  <li key={r.kind} className="mem-root">
                    <span className={"mem-kind " + kindColorClasses(r.kind)}>{r.label}</span>
                    <span className="muted">
                      {tr("mem.root_stats", {
                        files: r.files,
                        size: humanSize(r.bytes),
                      })}
                    </span>
                    {r.busy && <span className="mem-badge busy">{tr("mem.busy_badge")}</span>}
                    {r.toggleable && <OnOff value={!!r.enabled} onChange={(v) => void setCodexMemories(v)} />}
                    {r.projects.length > 0 && (
                      <span className="mem-projects">
                        {r.projects.map((p) => (
                          <span key={p.slug} className="mem-chip" title={p.slug}>
                            {p.display}
                          </span>
                        ))}
                      </span>
                    )}
                  </li>
                ))}
                {(data.inactive ?? []).map((r) => (
                  <li key={r.kind} className="mem-root mem-root-off">
                    <span className={"mem-kind " + kindColorClasses(r.kind)}>{r.label}</span>
                    <span className="muted">{reasonLabel(r.reason)}</span>
                    {r.toggleable && <OnOff value={!!r.enabled} onChange={(v) => void setCodexMemories(v)} />}
                  </li>
                ))}
              </ul>
            )}
            {/* Enabling this spends tokens continuously (background extraction and consolidation).
            Say so right next to the toggle so it does not look like "just a switch". */}
            {data?.inactive?.some((r) => r.toggleable && !r.enabled) && (
              <p className="muted ds-hint">{tr("mem.codex_cost_hint")}</p>
            )}
            {/* By design codex's own memory keeps running beside Agent Fleet memory (agent_memory_autosync.go
            leaves the flag as the member set it); say so, or the two read as one. */}
            {agentMemoryOn && [...(data?.roots ?? []), ...(data?.inactive ?? [])].some((r) => r.toggleable && r.enabled) && (
              <p className="muted ds-hint">{tr("mem.codex_dual_hint")}</p>
            )}
            <Row label={tr("mem.auto_label")}>
              <OnOff value={!!data?.auto} onChange={(v) => void setAuto(v)} />
            </Row>
            <p className="muted ds-hint">{data?.autoLocked ? tr("mem.auto_locked") : tr("mem.auto_hint")}</p>
          </section>
        )}

        {/* History and diffs */}
        {view === "history" && (
          <>
            <section className="mem-section">
              <div className="mem-head">
                <label className="mem-jump">
                  <span className="muted">{tr("mem.jump_at")}</span>
                  <input
                    type="datetime-local"
                    className="cinput"
                    value={at}
                    onChange={(e) => setAt(e.target.value)}
                  />
                  {/* A jump would select a row beside the list error; Retry is the way back. */}
                  <button type="button" disabled={!at || !!snapErr} onClick={() => void jumpTo()}>
                    {tr("mem.jump_go")}
                  </button>
                </label>
                <label className="mem-jump">
                  <input type="checkbox" checked={showAf} onChange={(e) => toggleAf(e.target.checked)} />
                  <span>{tr("mem.hist_show_af")}</span>
                </label>
              </div>
              <div className="mem-body">
                <ul className="mem-list">
                  {snaps === null ? (
                    <li className="muted pad">{tr("common.loading")}</li>
                  ) : snapErr ? (
                    <li className="mem-warn pad">
                      <span>{tr("mem.history_failed", { msg: snapErr })}</span>{" "}
                      <button type="button" className="linklike" onClick={() => setReload((n) => n + 1)}>
                        {tr("mem.history_retry")}
                      </button>
                    </li>
                  ) : snaps.length === 0 ? (
                    <li className="muted pad">{tr("mem.history_empty")}</li>
                  ) : (
                    rows.map((row) =>
                      "hidden" in row ? (
                        <li key={row.key} className="mem-snap-hidden muted pad">
                          <span>{tr("mem.hist_af_hidden", { n: row.hidden })}</span>{" "}
                          <button type="button" className="linklike" onClick={() => openSettings("memory:changes")}>
                            {tr("mem.hist_af_changes")}
                          </button>
                        </li>
                      ) : (
                        <li key={row.snap.rev}>
                          <button
                            type="button"
                            className={"mem-snap" + (row.snap.rev === sel ? " active" : "")}
                            aria-current={row.snap.rev === sel ? "true" : undefined}
                            onClick={() => setSel(row.snap.rev)}
                          >
                            <span className="mem-snap-when">{fmtDateTime(row.snap.at, DATETIME_FULL)}</span>
                            <span className={"mem-badge trig-" + row.snap.trigger}>
                              {triggerLabel(row.snap.trigger)}
                            </span>
                            <span className="mem-snap-what muted">
                              {row.snap.projects.length > 0
                                ? row.snap.projects.map((p) => p.display).join(tr("common.list_sep"))
                                : row.snap.kinds.join(tr("common.list_sep"))}
                              {" · "}
                              {tr("mem.n_files", { n: row.snap.files })}
                            </span>
                          </button>
                        </li>
                      ),
                    )
                  )}
                </ul>
                <div className="mem-diff">
                  {selected && (
                    <div className="mem-diff-head">
                      <code title={selected.rev}>{selected.short}</code>
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() =>
                          setScope({
                            rev: selected.rev,
                            all: true,
                            picked: {},
                            tree: null,
                          })
                        }
                      >
                        {tr("mem.restore")}
                      </button>
                    </div>
                  )}
                  {/* Nothing selected means nothing is being fetched: the loading text would never end. */}
                  {sel &&
                    (diff === null ? (
                      <pre className="diff muted">{tr("common.loading")}</pre>
                    ) : (
                      <Diff text={diff} embedded wrap />
                    ))}
                </div>
              </div>
            </section>

            {/* Choosing the restore scope */}
            {scope && (
              <RestorePanel
                state={scope}
                patch={(p) => setScope((cur) => (cur ? { ...cur, ...p } : cur))}
                onClose={() => setScope(null)}
                onSubmit={runRestore}
                busy={busy}
              />
            )}
          </>
        )}

        {/* Export & import */}
        {view === "transfer" && (
          <TransferSection busy={busy} setBusy={setBusy} onApplied={() => setReload((n) => n + 1)} />
        )}
      </div>
    </div>
  );
}
