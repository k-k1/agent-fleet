// SchedulesSection (docs/38 P5) — the left-rail view of operator-authored scheduled
// executions. Membership-scoped and Control-Plane persisted (like the memo queue), so it
// shows in both running and stopped workspace states and refetches on mount / tenant
// switch + slow-polls while mounted. Read + manage only: toggle enabled, run-now, view run
// history, delete. Creating/editing a schedule stays in the operator conversation because
// it needs the NL→cron translation the operator LLM does.
import { useEffect, useMemo, useRef, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import {
  scheduleList,
  scheduleRuns,
  schedulePause,
  scheduleResume,
  scheduleRunNow,
  scheduleDelete,
} from "./api.ts";
import {
  type ScheduleDTO,
  type ScheduleRun,
  scheduleTitle,
  specSummary,
  statusTone,
  statusIcon,
  sortSchedules,
} from "./read.ts";

const POLL_MS = 15000;

// Compact local timestamp for last_run / fired_at (RFC3339 UTC → the viewer's locale).
// next_run uses the server-rendered next_run_local (already in the schedule's own tz).
function shortLocal(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

export function SchedulesSection() {
  const tenant = useTenantStore((s) => s.tenant);
  const toast = useToast();
  const tr = useT();

  const [items, setItems] = useState<ScheduleDTO[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [openRuns, setOpenRuns] = useState<string | null>(null);
  const [runs, setRuns] = useState<Record<string, ScheduleRun[]>>({});
  const serRef = useRef("");

  // Refetch on mount / tenant switch, and poll while mounted (CP is pull-only — no push).
  useEffect(() => {
    let alive = true;
    const load = () =>
      scheduleList()
        .then((res) => {
          if (!alive) return;
          const arr = Array.isArray(res) ? (res as ScheduleDTO[]) : [];
          const ser = JSON.stringify(arr);
          if (ser !== serRef.current) {
            serRef.current = ser;
            setItems(arr);
          }
          setLoaded(true);
        })
        .catch(() => {});
    load();
    const id = setInterval(load, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [tenant]);

  const sorted = useMemo(() => sortSchedules(items), [items]);
  const activeCount = useMemo(() => items.filter((s) => s.enabled).length, [items]);

  const setRowBusy = (id: string, v: boolean) => setBusy((b) => ({ ...b, [id]: v }));

  // Apply a returned DTO to local state so the row updates without waiting for the poll.
  const applyDTO = (dto: ScheduleDTO | { error?: unknown }) => {
    if (dto && typeof dto === "object" && "id" in dto) {
      const s = dto as ScheduleDTO;
      setItems((cur) => cur.map((x) => (x.id === s.id ? s : x)));
      serRef.current = ""; // force the next poll to re-apply (order may have changed)
      return true;
    }
    return false;
  };

  const doToggle = async (s: ScheduleDTO) => {
    if (busy[s.id]) return;
    setRowBusy(s.id, true);
    try {
      const res = s.enabled ? await schedulePause(s.id) : await scheduleResume(s.id);
      if (!applyDTO(res)) {
        toast(t("sched.action_failed"), { kind: "warn" });
        return;
      }
      toast(s.enabled ? t("sched.paused") : t("sched.resumed"), { kind: "success" });
    } catch {
      toast(t("sched.action_failed"), { kind: "warn" });
    } finally {
      setRowBusy(s.id, false);
    }
  };

  const doRunNow = async (s: ScheduleDTO) => {
    if (busy[s.id]) return;
    setRowBusy(s.id, true);
    try {
      const res = await scheduleRunNow(s.id);
      if (!applyDTO(res)) {
        toast(t("sched.action_failed"), { kind: "warn" });
        return;
      }
      const warning = (res as ScheduleDTO).warning;
      if (warning) toast(warning, { kind: "warn", duration: 8000 });
      else toast(t("sched.run_now_queued"), { kind: "success" });
    } catch {
      toast(t("sched.action_failed"), { kind: "warn" });
    } finally {
      setRowBusy(s.id, false);
    }
  };

  const doDelete = async (s: ScheduleDTO) => {
    if (busy[s.id]) return;
    if (!confirm(t("sched.delete_confirm", { name: scheduleTitle(s) }))) return;
    setRowBusy(s.id, true);
    try {
      const r = await scheduleDelete(s.id);
      if (!r.ok) {
        toast(t("common.delete_failed"), { kind: "warn" });
        return;
      }
      setItems((cur) => cur.filter((x) => x.id !== s.id));
      if (openRuns === s.id) setOpenRuns(null);
      toast(t("sched.deleted"), { kind: "success" });
    } catch {
      toast(t("common.delete_failed"), { kind: "warn" });
    } finally {
      setRowBusy(s.id, false);
    }
  };

  const toggleRuns = async (s: ScheduleDTO) => {
    if (openRuns === s.id) {
      setOpenRuns(null);
      return;
    }
    setOpenRuns(s.id);
    try {
      const res = await scheduleRuns(s.id);
      const list = res && typeof res === "object" && "runs" in res ? (res as { runs: ScheduleRun[] }).runs : [];
      setRuns((r) => ({ ...r, [s.id]: Array.isArray(list) ? list : [] }));
    } catch {
      setRuns((r) => ({ ...r, [s.id]: [] }));
    }
  };

  return (
    <Section id="schedules" title={tr("sched.title")} icon="watch" count={activeCount}>
      {loaded && items.length === 0 ? (
        <div className="pane-empty">{tr("sched.empty")}</div>
      ) : (
        <div className="sched-list">
          {sorted.map((s) => {
            const tone = statusTone(s.last_status);
            const paused = !s.enabled;
            const rowBusy = !!busy[s.id];
            return (
              <div key={s.id} className={"sched-row" + (paused ? " paused" : "")}>
                <div className="sched-main">
                  <span
                    className={"sched-dot tone-" + tone}
                    title={s.last_status || tr("sched.never_run")}
                  >
                    <Icon name={statusIcon(s.last_status)} />
                  </span>
                  <div className="sched-info">
                    <div className="sched-name" title={scheduleTitle(s)}>
                      {scheduleTitle(s)}
                      {paused && <span className="sched-badge">{tr("sched.paused_tag")}</span>}
                    </div>
                    <div className="sched-sub">
                      <code className="sched-spec">{specSummary(s)}</code>
                      {s.agent_kind && <span className="sched-kind">{s.agent_kind}</span>}
                    </div>
                    <div className="sched-times">
                      {!paused && s.next_run_local && (
                        <span className="sched-next" title={tr("sched.next_run")}>
                          <Icon name="arrow-small-right" /> {s.next_run_local}
                        </span>
                      )}
                      {s.last_run && (
                        <span className="sched-last" title={tr("sched.last_run")}>
                          <Icon name="history" /> {shortLocal(s.last_run)}
                        </span>
                      )}
                    </div>
                  </div>
                  <div className="sched-actions">
                    <IconButton
                      icon={paused ? "debug-start" : "debug-pause"}
                      label={paused ? tr("sched.resume") : tr("sched.pause")}
                      disabled={rowBusy}
                      onClick={() => void doToggle(s)}
                    />
                    <IconButton
                      icon="play"
                      label={tr("sched.run_now")}
                      disabled={rowBusy || paused}
                      onClick={() => void doRunNow(s)}
                    />
                    <IconButton
                      icon="history"
                      label={tr("sched.history")}
                      className={openRuns === s.id ? "active" : ""}
                      onClick={() => void toggleRuns(s)}
                    />
                    <IconButton
                      variant="danger"
                      icon="trash"
                      label={tr("common.delete")}
                      disabled={rowBusy}
                      onClick={() => void doDelete(s)}
                    />
                  </div>
                </div>

                {openRuns === s.id && (
                  <div className="sched-runs">
                    {runs[s.id] === undefined ? (
                      <div className="sched-runs-empty">
                        <Icon name="loading" spin /> {tr("sched.loading")}
                      </div>
                    ) : runs[s.id].length === 0 ? (
                      <div className="sched-runs-empty">{tr("sched.no_runs")}</div>
                    ) : (
                      runs[s.id].map((run, i) => (
                        <div key={i} className="sched-run">
                          <span className={"sched-dot tone-" + statusTone(run.status)}>
                            <Icon name={statusIcon(run.status)} />
                          </span>
                          <span className="sched-run-time">{shortLocal(run.fired_at)}</span>
                          <span className="sched-run-status" title={run.detail || run.status}>
                            {run.status}
                          </span>
                        </div>
                      ))
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </Section>
  );
}
