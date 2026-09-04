// MemoryTab — version control for agent memory (docs/log/39 P2 / ADR 0022, adopted default #3).
//
// The persistent memory agents accumulate (claude's auto-memory, codex's memories) sits in
// storage that never goes away, yet it has no history. This tab is the operating surface for
// that history, its diffs and its rollbacks; the substance (a bare git repo) lives on the
// Agent side and this file only calls REST.
//
// Top to bottom: 1. root overview + auto-capture toggle + manual snapshot;
// 2. snapshot history (left) and the selected row's diff (right); 3. restore (pick a scope,
// then a confirm dialog); 4. export / import (P3: bundle = full history, tar.gz = latest only;
// import replaces the selected scope). The diff uses the same <Diff> as the SCM commit pane,
// so there is one way a diff looks rather than two.
//
// A rollback never rewrites history: a pre-restore snapshot is taken automatically before it
// applies (docs/log/39 ★2). Every action on this screen is therefore undoable, and the
// confirm-dialog wording is written on that assumption.

import { useCallback, useEffect, useState } from "react";
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

// Trigger label. The trailer value the Agent returns (auto/manual/pre-restore/restore/import)
// is used as the key directly and an unknown value is printed raw, so adding a new trigger
// upstream does not break this screen.
const triggerLabel = (trigger: string): string =>
  tMaybe("mem.trigger_" + trigger.replace(/-/g, "_")) ?? trigger ?? "";

// Why a root is inactive. Same rule as the trigger label: an unknown reason is printed raw,
// so a new reason from the Agent does not break the screen.
const reasonLabel = (reason: string): string => tMaybe("mem.reason_" + reason) ?? reason ?? "";

export function MemoryTab() {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";

  const [data, setData] = useState<RootsPayload | null>(null);
  const [snaps, setSnaps] = useState<Snapshot[] | null>(null);
  const [err, setErr] = useState("");
  const [reload, setReload] = useState(0);
  const [busy, setBusy] = useState(false);

  // The selected snapshot (a history row) and its diff.
  const [sel, setSel] = useState("");
  const [diff, setDiff] = useState<string | null>(null);
  // Jump-to-timestamp (the datetime-local value).
  const [at, setAt] = useState("");
  // The restore panel. null = closed.
  const [scope, setScope] = useState<RestoreScopeState | null>(null);

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
        setErr(errText(r.error));
        return true;
      }
      setErr("");
      setData(r);
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
    setSel((cur) => (cur && snaps.some((s) => s.rev === cur) ? cur : snaps[0].rev));
  }, [snaps]);

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
    const res = await apiJSON("api/agents/memory/settings", "PUT", { auto: on });
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
      const res = await apiJSON("api/agents/memory/snapshots", "POST", { trigger: "manual" });
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
      const res = await apiJSON(
        "api/agents/memory/restore?rev=" + encodeURIComponent(rev),
        "POST",
        body,
      );
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

  return (
    <div className="mem-tab">
      <p className="muted ds-note">{tr("mem.intro")}</p>
      {err && <p className="mem-warn">{err}</p>}

      {/* ① 対象と取得契機 */}
      <section className="mem-section">
        <div className="mem-head">
          <h3>{tr("mem.roots_title")}</h3>
          <span className="muted">
            {data?.lastSnapshot
              ? tr("mem.last_snapshot", { when: fmtDateTime(data.lastSnapshot, DATETIME_FULL) })
              : tr("mem.never")}
          </span>
          <button type="button" disabled={busy} onClick={snapshotNow}>
            {tr("mem.snapshot_now")}
          </button>
        </div>
        {!data ? (
          <p className="muted pad">{tr("common.loading")}</p>
        ) : data.roots.length === 0 && !data.inactive?.length ? (
          <p className="muted pad">{tr("mem.no_roots")}</p>
        ) : (
          <ul className="mem-roots">
            {data.roots.map((r) => (
              <li key={r.kind} className="mem-root">
                <span className={"mem-kind kind-" + r.kind}>{r.label}</span>
                <span className="muted">
                  {tr("mem.root_stats", { files: r.files, size: humanSize(r.bytes) })}
                </span>
                {r.busy && <span className="mem-badge busy">{tr("mem.busy_badge")}</span>}
                {r.toggleable && (
                  <OnOff value={!!r.enabled} onChange={(v) => void setCodexMemories(v)} />
                )}
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
                <span className={"mem-kind kind-" + r.kind}>{r.label}</span>
                <span className="muted">{reasonLabel(r.reason)}</span>
                {r.toggleable && <OnOff value={!!r.enabled} onChange={(v) => void setCodexMemories(v)} />}
              </li>
            ))}
          </ul>
        )}
        {/* 有効化はトークンを継続的に消費する（バックグラウンドの抽出・統合）。
            トグルの隣で必ず伝える — 「ただのスイッチ」に見せない。 */}
        {data?.inactive?.some((r) => r.toggleable && !r.enabled) && (
          <p className="muted ds-hint">{tr("mem.codex_cost_hint")}</p>
        )}
        <Row label={tr("mem.auto_label")}>
          <OnOff value={!!data?.auto} onChange={(v) => void setAuto(v)} />
        </Row>
        <p className="muted ds-hint">
          {data?.autoLocked ? tr("mem.auto_locked") : tr("mem.auto_hint")}
        </p>
      </section>

      {/* ② 履歴と差分 */}
      <section className="mem-section">
        <div className="mem-head">
          <h3>{tr("mem.history_title")}</h3>
          <label className="mem-jump">
            <span className="muted">{tr("mem.jump_at")}</span>
            <input
              type="datetime-local"
              className="cinput"
              value={at}
              onChange={(e) => setAt(e.target.value)}
            />
            <button type="button" disabled={!at} onClick={() => void jumpTo()}>
              {tr("mem.jump_go")}
            </button>
          </label>
        </div>
        <div className="mem-body">
          <ul className="mem-list">
            {snaps === null ? (
              <li className="muted pad">{tr("common.loading")}</li>
            ) : snaps.length === 0 ? (
              <li className="muted pad">{tr("mem.history_empty")}</li>
            ) : (
              snaps.map((s) => (
                <li key={s.rev}>
                  <button
                    type="button"
                    className={"mem-snap" + (s.rev === sel ? " active" : "")}
                    aria-current={s.rev === sel ? "true" : undefined}
                    onClick={() => setSel(s.rev)}
                  >
                    <span className="mem-snap-when">{fmtDateTime(s.at, DATETIME_FULL)}</span>
                    <span className={"mem-badge trig-" + s.trigger}>{triggerLabel(s.trigger)}</span>
                    <span className="mem-snap-what muted">
                      {s.projects.length > 0
                        ? s.projects.map((p) => p.display).join(tr("common.list_sep"))
                        : s.kinds.join(tr("common.list_sep"))}
                      {" · "}
                      {tr("mem.n_files", { n: s.files })}
                    </span>
                  </button>
                </li>
              ))
            )}
          </ul>
          <div className="mem-diff">
            {selected && (
              <div className="mem-diff-head">
                <code title={selected.rev}>{selected.short}</code>
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => setScope({ rev: selected.rev, all: true, picked: {}, tree: null })}
                >
                  {tr("mem.restore")}
                </button>
              </div>
            )}
            {diff === null ? (
              <pre className="diff muted">{tr("common.loading")}</pre>
            ) : (
              <Diff text={diff} embedded wrap />
            )}
          </div>
        </div>
      </section>

      {/* ③ 戻し範囲の選択 */}
      {scope && (
        <RestorePanel
          state={scope}
          patch={(p) => setScope((cur) => (cur ? { ...cur, ...p } : cur))}
          onClose={() => setScope(null)}
          onSubmit={runRestore}
          busy={busy}
        />
      )}

      {/* ④ 持ち出し / 取り込み */}
      <TransferSection busy={busy} setBusy={setBusy} onApplied={() => setReload((n) => n + 1)} />
    </div>
  );
}
