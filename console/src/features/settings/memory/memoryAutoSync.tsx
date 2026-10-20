// The Agent memory switch carries claude's native memory across by itself (#1972, ADR 0108
// decision 6): switching on imports it, switching off writes AF memory back. The Agent does the
// work in the background, so this file only does three things:
//
//   - asks before the flip, with the counts a preview of that run gives (the member sees what
//     will happen first);
//   - shows where the run is and what it left: conflicts, skipped secrets, and a failed
//     write-back with the way out ("switch native memory back on anyway");
//   - wakes the Agent after a confirmed flip, which is also what leaves the member's decision in
//     the audit ledger (the run itself never passes through the Control Plane).
//
// The flip is a ui-pref, so it stays reachable while the workspace is stopped: with no Agent to
// ask there are no counts, and the Agent reconciles when it starts.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { PreviewSection } from "./memoryWizard.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { setSetting, useSettings } from "../../../lib/settings.ts";
import { useT, tMaybe } from "../../../lib/i18n/index.ts";
import type { AutoSyncPreview, AutoSyncRun, AutoSyncStatus } from "./memoryTypes.ts";

const POLL_MS = 3000;
// An unknown status or reason from a newer Agent is printed raw rather than breaking the row.
const reasonLabel = (r: string): string => tMaybe("mem.as_reason_" + r) ?? tMaybe("mem.ce_reason_" + r) ?? tMaybe("mem.ci_reason_" + r) ?? r;
const statusLabel = (s: string): string => tMaybe("mem.as_status_" + s) ?? s;

const sum = (run: AutoSyncRun, key: string): number => run.projects.reduce((n, p) => n + (p.counts[key] ?? 0), 0);

/** flipAgentMemory asks first (with the preview's counts when the Agent can give them), then
 *  flips the switch and wakes the Agent. It resolves true when the switch was flipped. */
export function useFlipAgentMemory(): (next: boolean) => Promise<boolean> {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const running = useWorkspaceStore((s) => s.state === "running");
  return useCallback(
    async (next: boolean) => {
      const to = next ? "on" : "off";
      let preview: AutoSyncPreview | null = null;
      if (running) {
        const res = await api("api/agents/memory/auto-sync/preview?to=" + to);
        if (!res?.error) preview = res as AutoSyncPreview;
      }
      const t = preview?.totals ?? {};
      const detail: string[] = [];
      if (preview) {
        if (next) {
          if (t.secret) detail.push(tr("mem.as_confirm_secret", { n: t.secret }));
          if (t.conflict) detail.push(tr("mem.as_confirm_conflict", { n: t.conflict }));
          if (preview.unmapped) detail.push(tr("mem.as_unmapped", { n: preview.unmapped }));
        } else {
          if (t.conflict) detail.push(tr("mem.as_confirm_conflict_off", { n: t.conflict }));
          if (t.secret) detail.push(tr("mem.as_confirm_secret_off", { n: t.secret }));
          if (preview.noRoot) detail.push(tr("mem.as_no_root", { n: preview.noRoot }));
        }
      }
      const ok = await askConfirm({
        title: tr(next ? "mem.as_confirm_on_title" : "mem.as_confirm_off_title"),
        body: (
          <>
            <p>
              {preview
                ? tr(next ? "mem.as_confirm_on_body" : "mem.as_confirm_off_body", {
                    n: preview.apply,
                    projects: preview.projects.length,
                  })
                : tr(running ? "mem.as_confirm_unknown" : "mem.as_confirm_stopped")}
            </p>
            {detail.map((d) => (
              <p key={d} className="muted">
                {d}
              </p>
            ))}
            <p className="muted">{tr(next ? "mem.as_confirm_on_native" : "mem.as_confirm_off_native")}</p>
          </>
        ),
        confirmLabel: tr(next ? "mem.as_confirm_on_label" : "mem.as_confirm_off_label"),
      });
      if (!ok) return false;
      setSetting("agentMemory", next);
      if (running) {
        // The ui-pref reaches the Agent on its own (and wakes the run there). This call is the
        // member's confirmation reaching the audit ledger, and it starts the run without waiting
        // for the next settings sync.
        const res = await apiJSON("api/agents/memory/auto-sync?action=wake&to=" + to, "POST", { action: "wake" });
        if (res?.error) toast(errDetail(res.error));
      }
      return true;
    },
    [askConfirm, running, toast, tr],
  );
}

/** AutoSyncPanel shows the reconciler's state and the last run under the switch. */
export function AutoSyncPanel() {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const running = useWorkspaceStore((s) => s.state === "running");
  // The switch as this Console holds it: the Agent reads it once the settings sync lands, so the
  // panel keeps reading until the Agent has caught up with it.
  const enabled = useSettings().agentMemory;
  const [st, setSt] = useState<AutoSyncStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const refresh = useCallback(async () => {
    const res = await api("api/agents/memory/auto-sync");
    if (alive.current && !res?.error) setSt(res as AutoSyncStatus);
    return res as AutoSyncStatus | undefined;
  }, []);

  // Poll while a run is going or the Agent is still catching up with the switch; read once otherwise.
  useEffect(() => {
    if (!running) {
      setSt(null);
      return;
    }
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      const cur = await refresh();
      if (!alive.current) return;
      if (cur && (cur.want !== enabled || !cur.settled || cur.phase === "writing_back" || (cur.catchUpPending ?? 0) > 0)) {
        timer = setTimeout(() => void tick(), POLL_MS);
      }
    };
    void tick();
    return () => clearTimeout(timer);
  }, [running, refresh, enabled]);

  const act = async (action: "retry" | "abandon") => {
    setBusy(true);
    try {
      const res = await apiJSON("api/agents/memory/auto-sync?action=" + action, "POST", { action });
      if (res?.error) toast(errDetail(res.error));
      await refresh();
    } finally {
      setBusy(false);
    }
  };
  const abandon = async () => {
    const ok = await askConfirm({
      title: tr("mem.as_abandon_title"),
      body: <p>{tr("mem.as_abandon_body")}</p>,
      confirmLabel: tr("mem.as_abandon"),
      danger: true,
    });
    if (ok) await act("abandon");
  };

  if (!running || !st) return null;
  const run = st.run;
  const unreadable = st.phase === "unreadable";
  const headKey = unreadable
    ? "mem.as_phase_unreadable"
    : !st.settled
    ? st.phase === "importing" || (st.want && st.phase !== "on")
      ? "mem.as_phase_importing"
      : "mem.as_phase_writing_back"
    : st.phase === "on"
      ? "mem.as_phase_on"
      : "mem.as_phase_off";
  const rows = run?.rows ?? [];
  return (
    <div aria-live="polite">
      <p className="muted ds-note">{tr(headKey)}</p>
      {st.nativeBackOn && st.settled && !st.want && <p className="muted ds-note">{tr("mem.as_native_back_on")}</p>}
      {(st.catchUpPending ?? 0) > 0 && (
        <p className="muted ds-note">{tr("mem.as_catchup_pending", { n: st.catchUpPending ?? 0 })}</p>
      )}
      {(st.problems ?? []).map((p) => (
        <p key={p} className="mem-warn">
          {tMaybe("mem.as_problem_" + p) ?? p}
        </p>
      ))}
      {st.failing && (
        <div className="mem-warn">
          {!unreadable && <p>{tr("mem.as_failing", { error: run?.error ?? "" })}</p>}
          <div className="mem-ci-actions">
            {!unreadable && (
              <Button disabled={busy} onClick={() => void act("retry")}>
                {tr("mem.as_retry")}
              </Button>
            )}
            <Button disabled={busy} onClick={() => void abandon()}>
              {tr("mem.as_abandon")}
            </Button>
          </div>
        </div>
      )}
      {run && run.finished && (
        <p className="muted ds-note">
          {run.kind === "write_back"
            ? tr("mem.as_last_write_back", {
                written: sum(run, "written"),
                updated: sum(run, "updated"),
                removed: sum(run, "removed"),
                unchanged: sum(run, "unchanged"),
              })
            : tr(run.kind === "catch_up" ? "mem.as_last_catch_up" : "mem.as_last_import", {
                imported: sum(run, "imported"),
                updated: sum(run, "updated"),
                unchanged: sum(run, "unchanged"),
              })}
        </p>
      )}
      {(run?.unmapped ?? 0) > 0 && <p className="muted ds-note">{tr("mem.as_unmapped", { n: run?.unmapped ?? 0 })}</p>}
      {(run?.noRoot ?? 0) > 0 && <p className="muted ds-note">{tr("mem.as_no_root", { n: run?.noRoot ?? 0 })}</p>}
      {rows.length > 0 && (
        <PreviewSection summary={tr("mem.as_rows", { n: rows.length + (run?.rowsCut ?? 0) })} open>
          <ul className="mem-ci-items">
            {rows.map((r, i) => (
              <li key={r.project + "/" + r.name + i}>
                <span className="mem-af-name">{r.name}</span>{" "}
                <span className="muted">
                  · {statusLabel(r.status)}
                  {r.reason ? " · " + reasonLabel(r.reason) : ""}
                  {r.rules?.length ? " · " + r.rules.join(", ") : ""}
                </span>
              </li>
            ))}
          </ul>
          {(run?.rowsCut ?? 0) > 0 && <p className="muted">{tr("mem.as_rows_cut", { n: run?.rowsCut ?? 0 })}</p>}
        </PreviewSection>
      )}
    </div>
  );
}
