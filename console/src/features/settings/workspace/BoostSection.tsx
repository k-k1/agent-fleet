import { useCallback, useEffect, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { Button } from "../../../ui/Button.tsx";
import { ConfirmDialog } from "../../../ui/ConfirmDialog.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { Row } from "../parts/controls.tsx";
import { boostReasonKey, boxText, type WsSizingView } from "./boost.ts";
import { cancelBoost, getWorkspaceSizing, pressBoost } from "./workspaceApi.ts";

// useSizing reads the CP's sizing answer, and re-reads it whenever the workspace changes state:
// a Stop → Start is exactly when the next-start box becomes the running one.
export function useSizing(): { d: WsSizingView | null; reload: () => Promise<void> } {
  const wsState = useWorkspaceStore((s) => s.state);
  const [d, setD] = useState<WsSizingView | null>(null);
  const reload = useCallback(async () => {
    try {
      const r = await getWorkspaceSizing();
      if (r && !r.error) setD(r);
    } catch {
      /* the section keeps its last answer */
    }
  }, []);
  useEffect(() => {
    void reload();
  }, [reload, wsState]);
  return { d, reload };
}

// BoostSection — which box the next start uses and why, the Boost action, and the restart
// offer. Everything it says comes from the CP; the box is never recomputed here.
export function BoostSection({ sizing }: { sizing: { d: WsSizingView | null; reload: () => Promise<void> } }) {
  const { d, reload } = sizing;
  if (!d || !d.supported) return null;
  return <BoostView d={d} reload={reload} />;
}

export function BoostView({ d, reload }: { d: WsSizingView; reload: () => Promise<void> }) {
  const tr = useT();
  const toast = useToast();
  const restart = useWorkspaceStore((s) => s.restart);
  const [hours, setHours] = useState(d.boost.default_hours);
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const next = d.next_start;
  const boost = d.boost;
  const blocked = boostReasonKey(boost);

  const press = async () => {
    setBusy(true);
    try {
      const r = await pressBoost(hours);
      if (r?.error) toast(errText(r.error), { kind: "error" });
      await reload();
    } finally {
      setBusy(false);
    }
  };
  const cancel = async () => {
    setBusy(true);
    try {
      const r = await cancelBoost();
      if (r?.error) toast(errText(r.error), { kind: "error" });
      await reload();
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="ds-group boost-section">
      <h4 className="ds-title">{tr("boost.title")}</h4>
      {next && (
        <Row label={tr("boost.next_start")}>
          <span className="mv-val">
            {boxText(next.box)}
            {next.source !== "stored" && (
              <span className="mv-src">
                {next.source === "auto"
                  ? tr("boost.source_auto")
                  : tr("boost.source_boost", { until: next.until ? fmtDateTime(next.until) : "" })}
              </span>
            )}
          </span>
        </Row>
      )}
      {d.running && (
        <Row label={tr("boost.running")}>
          <span className="mv-val">{boxText(d.running)}</span>
        </Row>
      )}
      {next?.note && <p className="muted ds-note">{next.note}</p>}

      {d.restart_offered && (
        <p className="ds-note boost-restart">
          {tr("boost.restart_offer", { box: boxText(next?.box) })}{" "}
          <Button disabled={busy} onClick={() => setConfirm(true)}>
            {tr("boost.restart")}
          </Button>
        </p>
      )}
      {!d.restart_offered && d.running && next && (next.box.mem_mib !== d.running.mem_mib || next.box.class !== d.running.class) && (
        <p className="muted ds-note">{tr("boost.applies_next_start")}</p>
      )}
      {d.running && next && next.box.arch && d.running.arch && next.box.arch !== d.running.arch && (
        <p className="muted ds-note">{tr("boost.arch_change")}</p>
      )}

      {boost.enabled || boost.active ? (
        <>
          <div className="boost-actions">
            {boost.available || boost.active ? (
              <>
                <label className="boost-hours">
                  {tr("boost.hours")}
                  <input
                    type="number"
                    min={1}
                    max={boost.max_hours}
                    value={hours}
                    onChange={(e) => setHours(Math.max(1, Math.min(boost.max_hours, +e.target.value || 1)))}
                  />
                </label>
                <Button disabled={busy || !boost.available} onClick={press}>
                  {tr(boost.active ? "boost.extend" : "boost.press", { box: boxText(boost.target) })}
                </Button>
              </>
            ) : (
              <Button disabled>{boost.target ? tr("boost.press", { box: boxText(boost.target) }) : tr("boost.press_plain")}</Button>
            )}
            {boost.active && (
              <Button disabled={busy} onClick={cancel}>
                {tr("boost.cancel")}
              </Button>
            )}
          </div>
          {blocked && <p className="muted ds-note">{tr(blocked)}</p>}
          {boost.budget.monthly_hours > 0 && (
            <p className="muted ds-note">
              {boost.budget.counted
                ? tr("boost.budget_used", { used: boost.budget.used_hours.toFixed(1), total: String(boost.budget.monthly_hours) })
                : tr("boost.budget_uncounted", { total: String(boost.budget.monthly_hours) })}
            </p>
          )}
          <p className="muted ds-note">{tr("boost.note")}</p>
        </>
      ) : null}
      {confirm && (
        <ConfirmDialog
          title={tr("boost.restart_title")}
          confirmLabel={tr("boost.restart")}
          busy={busy}
          onConfirm={() => {
            setConfirm(false);
            void restart();
          }}
          onCancel={() => setConfirm(false)}
        >
          <p>{tr("boost.restart_body", { box: boxText(next?.box) })}</p>
        </ConfirmDialog>
      )}
    </section>
  );
}
