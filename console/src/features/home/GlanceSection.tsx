// "At a glance" on Home (#2362): machine, running time, agents, model usage and subscription
// limits as one row of tiles, each opening the view that holds the detail. No figure is invented
// here; every one is read from the source its detailed view reads, and the rules those views
// hold (zero is not unmeasured, stale is stated, cost is an estimate) are kept by glance.ts and
// the formatters below.
//
// COST WHEN NOBODY LOOKS: nothing in this file runs until the section is expanded. The body is
// a separate component, so folding Home's section unmounts the stats-feed viewer (the fallback
// poll in wsStatsFeed.ts counts viewers), the uptime read and the usage-series read.
//
// LIMITS ARE READ FROM THE CHIPS' STORE, NEVER POLLED. The WS bar chips own the one poll of the
// usage-limit endpoints and write every reading to usageReadings.ts; mounting Home adds no request.
import { useCallback, useEffect, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { Sparkline } from "../../ui/Sparkline.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { fmtGiB } from "../../lib/bytes.ts";
import { fmtTok } from "../../lib/fmttok.ts";
import { useIsMobile } from "../../lib/device.ts";
import { kindLabel } from "../../lib/sessionkind.ts";
import { api, errText, isTransientErr } from "../../core/api/client.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useWsStatsSeries } from "../../core/store/wsStatsFeed.ts";
import { agoText } from "../../app/wsbar/usageCommon.tsx";
import { readingsFor, useUsageReadings } from "../../app/wsbar/usageReadings.ts";
import { useSettingsUI } from "../settings/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { sessionTier, TIER_WAITING } from "../sessions/order.ts";
import { openSessionsOverview } from "../overview/open.ts";
import { fetchUsageSeries, type UsageSeries } from "../usage/api.ts";
import { dimLabel, fmtUSDEst } from "../usage/UsageView.tsx";
import { rangeOf } from "../usage/series.ts";
import { localDayKey, minutesOf, widenForTimezone, type UptimeResponse } from "../usage/uptime.ts";
import "../usage/uptime.css";
import {
  limitRows,
  machineSummary,
  modelUsage,
  UPTIME_DAYS,
  uptimeSummary,
  type LimitRowModel,
  type ModelUsage,
  type UptimeSummary,
} from "./glance.ts";

// The fold state is this browser's, per user: a plain toggle until #2032's section registry
// lands. Unset = open on desktop, folded on a phone.
const KEY = "af-home-glance";

function readOpen(mobile: boolean): boolean {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "1") return true;
    if (v === "0") return false;
  } catch {
    /* storage unavailable: fall through to the default */
  }
  return !mobile;
}

function usePageVisible(): boolean {
  const [visible, setVisible] = useState(() => typeof document === "undefined" || !document.hidden);
  useEffect(() => {
    const on = () => setVisible(!document.hidden);
    document.addEventListener("visibilitychange", on);
    return () => document.removeEventListener("visibilitychange", on);
  }, []);
  return visible;
}

export function GlanceSection() {
  const tr = useT();
  const mobile = useIsMobile();
  const [stored, setStored] = useState<boolean | null>(null);
  const open = stored ?? readOpen(mobile);
  const toggle = () => {
    const next = !open;
    setStored(next);
    try {
      localStorage.setItem(KEY, next ? "1" : "0");
    } catch {
      /* the toggle still works for this page view */
    }
  };
  return (
    <section className="home-section home-glance">
      <h3 className="home-title" title={tr("home.glance_hint")}>
        <button type="button" className="home-glance-toggle" aria-expanded={open} onClick={toggle}>
          <Icon name={open ? "chevron-down" : "chevron-right"} />
          <Icon name="dashboard" /> {tr("home.glance")}
        </button>
      </h3>
      {open && <GlanceBody />}
    </section>
  );
}

function useLoad<T>(enabled: boolean, load: (signal: AbortSignal) => Promise<T | null>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [failed, setFailed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    const ac = new AbortController();
    setLoading(true);
    setFailed(false);
    load(ac.signal)
      .then((d) => {
        if (ac.signal.aborted) return;
        setData(d);
        setFailed(d == null);
      })
      .catch(() => !ac.signal.aborted && setFailed(true))
      .finally(() => !ac.signal.aborted && setLoading(false));
    return () => ac.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, tick, ...deps]);
  return { data, failed, loading, reload: useCallback(() => setTick((n) => n + 1), []) };
}

function GlanceBody() {
  const tr = useT();
  const running = useWorkspaceStore((s) => s.state) === "running";
  const tenant = useTenantStore((s) => s.tenant);
  const visible = usePageVisible();
  const sessions = useSessionsStore((s) => s.sessions);
  const byKind = useUsageReadings((s) => readingsFor(s, tenant));

  // One request for the 7 days, on mount; the CP answers while the workspace is stopped.
  const uptime = useLoad<UptimeSummary>(
    true,
    async () => {
      const now = Date.now();
      const w = widenForTimezone(localDayKey(new Date(now - (UPTIME_DAYS - 1) * 86400000)), localDayKey(new Date(now)));
      const d = await api(`api/usage/me/hourly?${new URLSearchParams(w).toString()}`);
      return d && !d.error ? uptimeSummary(d as UptimeResponse, now) : null;
    },
    [tenant],
  );

  // One series request: 24 h, hourly buckets, by model, never forced. The Agent is the source,
  // so nothing is asked while the workspace is stopped.
  const models = useLoad<ModelUsage>(
    running,
    async (signal) => {
      const { from, to, bucket } = rangeOf(24, new Date());
      const d = await fetchUsageSeries({ from, to, bucket, by: "model" }, signal);
      if ((d as { error?: unknown }).error) {
        if (isTransientErr(d)) return null;
        throw new Error(errText((d as { error: Parameters<typeof errText>[0] }).error));
      }
      return modelUsage(d as UsageSeries);
    },
    [tenant],
  );

  const limits = limitRows(byKind, Date.now(), tr);
  const live = sessions.filter((s) => s.alive);
  const waiting = live.filter((s) => sessionTier(s) === TIER_WAITING).length;
  const busy = live.filter((s) => s.state === "working").length;

  const showUptime = !!uptime.data?.observed;
  const showModels = running && (models.failed || models.data?.state !== "nothing") && !(models.loading && !models.data);
  const tiles =
    1 + // machine: a stopped workspace says so
    (showUptime ? 1 : 0) +
    (live.length ? 1 : 0) +
    (running ? (showModels ? 1 : 0) : 1) +
    (limits.length ? 1 : 0);

  return (
    <div className="glance-grid">
      {(running ? visible : true) && <MachineTile running={running} />}
      {showUptime && uptime.data && <UptimeTile s={uptime.data} />}
      {live.length > 0 && <AgentsTile running={live.length - waiting} busy={busy} waiting={waiting} />}
      {!running ? (
        <Tile id="models" icon="graph" title={tr("home.glance_models")} open={() => useSettingsUI.getState().openSettings("usage")}>
          <p className="glance-note">{tr("home.glance_stopped")}</p>
        </Tile>
      ) : (
        showModels && (
          <ModelsTile m={models.data} failed={models.failed} loading={models.loading} reload={models.reload} />
        )
      )}
      {limits.length > 0 && <LimitsTile rows={limits} />}
      {tiles === 0 && <p className="home-empty">{tr("home.glance_empty")}</p>}
    </div>
  );
}

function Tile({
  id,
  icon,
  title,
  open,
  children,
  extra,
}: {
  id: string;
  icon: string;
  title: string;
  open: () => void;
  children: React.ReactNode;
  extra?: React.ReactNode;
}) {
  return (
    <div className={"glance-tile glance-t-" + id} data-tile={id}>
      <div className="glance-head">
        <Button variant="ghost" small className="glance-link" icon={icon} onClick={open}>
          {title}
        </Button>
        {extra}
      </div>
      {children}
    </div>
  );
}

function MachineTile({ running }: { running: boolean }) {
  const tr = useT();
  const open = () => useSettingsUI.getState().openSettings("machine");
  const title = tr("home.glance_machine");
  if (!running)
    return (
      <Tile id="machine" icon="server" title={title} open={open}>
        <p className="glance-note">{tr("home.glance_stopped")}</p>
      </Tile>
    );
  // Mounting MachineLive is what registers the viewer with wsStatsFeed.
  return <MachineLive title={title} open={open} />;
}

function MachineLive({ title, open }: { title: string; open: () => void }) {
  const tr = useT();
  const samples = useWsStatsSeries();
  const m = machineSummary(samples);
  const gib = (b: number) => fmtGiB(b) + " GiB";
  const pct = (a: number | null, b: number | null) => (a != null && b ? Math.round((a / b) * 100) : null);
  return (
    <Tile id="machine" icon="server" title={title} open={open}>
      {m.oom && <p className="glance-warn">{tr("machine.usage_oom")}</p>}
      {m.state !== "ok" ? (
        <p className="glance-note">{tr("home.glance_nodata")}</p>
      ) : (
        <dl className="glance-kv">
          <dt>CPU</dt>
          <dd>
            {m.cpu != null ? `${Math.round(m.cpu)}%` : "—"}
            <Sparkline data={samples.slice(-60).map((s) => s.cpu)} width={64} height={16} />
          </dd>
          <dt>{tr("machine.memory")}</dt>
          <dd>
            {m.memUsed != null ? `${gib(m.memUsed)}${m.memMax ? " / " + gib(m.memMax) : ""}` : "—"}
            {pct(m.memUsed, m.memMax) != null && <span className="muted"> ({pct(m.memUsed, m.memMax)}%)</span>}
          </dd>
          <dt>{tr("home.glance_disk")}</dt>
          <dd>{m.diskUsed != null && m.diskTotal ? `${gib(m.diskUsed)} / ${gib(m.diskTotal)}` : "—"}</dd>
        </dl>
      )}
    </Tile>
  );
}

const dur = (secs: number, tr: ReturnType<typeof useT>): string => {
  const m = minutesOf(secs);
  return m >= 60 ? tr("home.glance_dur_hm", { h: Math.floor(m / 60), m: m % 60 }) : tr("home.glance_dur_m", { m });
};

function UptimeTile({ s }: { s: UptimeSummary }) {
  const tr = useT();
  const stateText = (st: string) => (st === "unobserved" ? tr("uptime.state_unobserved") : st === "stopped" ? tr("uptime.state_stopped") : "");
  return (
    <Tile id="uptime" icon="watch" title={tr("home.glance_uptime")} open={() => useSettingsUI.getState().openSettings("uptime")}>
      <dl className="glance-kv">
        <dt>{tr("home.glance_today")}</dt>
        <dd>{dur(s.todaySecs, tr)}</dd>
        <dt>{tr("home.glance_week")}</dt>
        <dd>{dur(s.weekSecs, tr)}</dd>
      </dl>
      {/* The heatmap's three cell states: outline = the CP did not observe the day, grey =
          observed and stopped, coloured = running. State is also in the label. */}
      <div className="glance-strip" role="list">
        {s.days.map((d) => (
          <span
            key={d.day}
            role="listitem"
            className={"uh-cell " + (d.state === "unobserved" ? "uh-unobserved" : d.state === "stopped" ? "uh-stopped" : "uh-lv-" + d.level)}
            aria-label={`${d.day} ${d.state === "running" ? dur(d.runningSecs, tr) : stateText(d.state)}`}
            title={`${d.day} ${d.state === "running" ? dur(d.runningSecs, tr) : stateText(d.state)}`}
          />
        ))}
      </div>
      {s.idleRunningSecs > 0 && <p className="glance-note">{tr("home.glance_idle", { time: dur(s.idleRunningSecs, tr) })}</p>}
    </Tile>
  );
}

function AgentsTile({ running, busy, waiting }: { running: number; busy: number; waiting: number }) {
  const tr = useT();
  return (
    <Tile id="agents" icon="hubot" title={tr("home.glance_agents")} open={openSessionsOverview}>
      <p className="glance-line">{tr("home.glance_agents_line", { running, busy, waiting })}</p>
    </Tile>
  );
}

function ModelsTile({ m, failed, loading, reload }: { m: ModelUsage | null; failed: boolean; loading: boolean; reload: () => void }) {
  const tr = useT();
  return (
    <Tile
      id="models"
      icon="graph"
      title={tr("home.glance_models")}
      open={() => useSettingsUI.getState().openSettings("usage")}
      extra={
        <Button variant="ghost" small icon="refresh" title={tr("home.glance_refresh")} aria-label={tr("home.glance_refresh")} onClick={reload} disabled={loading} />
      }
    >
      <p className="glance-sub muted">{tr("home.glance_models_range")}</p>
      {failed || !m ? (
        <p className="glance-note">{failed ? tr("home.glance_load_error") : "—"}</p>
      ) : (
        <>
          {m.folding && <p className="glance-badge">{tr("home.glance_folding")}</p>}
          {m.state === "measured" ? (
            <ul className="glance-models">
              {m.rows.map((r) => (
                <li key={r.key}>
                  <span className="glance-model">{dimLabel("model", r.key)}</span>
                  <span>{fmtTok(r.spend)}</span>
                </li>
              ))}
              {m.other.count > 0 && (
                <li className="muted">
                  <span className="glance-model">{tr("home.glance_other", { n: m.other.count })}</span>
                  <span>{fmtTok(m.other.spend)}</span>
                </li>
              )}
            </ul>
          ) : (
            <p className="glance-line">—</p>
          )}
          {m.unmeasured > 0 && <p className="glance-note">{tr("usage.coverage_unmeasured", { n: m.unmeasured })}</p>}
          {m.costEst > 0 && (
            <p className="glance-sub muted" title={tr("usage.kpi_cost_hint")}>
              {tr("usage.kpi_cost")}: {fmtUSDEst(m.costEst)}
            </p>
          )}
        </>
      )}
    </Tile>
  );
}

function LimitsTile({ rows }: { rows: LimitRowModel[] }) {
  const tr = useT();
  return (
    <Tile id="limits" icon="pulse" title={tr("home.glance_limits")} open={() => useSettingsUI.getState().openSettings("usage")}>
      {rows.map((r) => (
        <div key={r.kind} className="glance-limit" data-kind={r.kind}>
          <div className="glance-limit-head">
            <span>{kindLabel(r.kind)}</span>
            {r.manageURL && (
              <a href={r.manageURL} target="_blank" rel="noopener" title={tr("wsbar.usage.open_page")}>
                <Icon name="link-external" />
              </a>
            )}
          </div>
          {r.windows.map((w, i) => (
            <div key={i} className="glance-win">
              <span className="muted">{w.label}</span>
              <span className={w.pct == null ? "muted" : w.pct >= 95 ? "crit" : w.pct >= 80 ? "warn" : ""}>
                {w.pct == null ? "—" : `${w.pct}%`}
              </span>
              {w.note && (
                <span className="glance-win-note muted">
                  {w.note === "stale" ? tr("wsbar.usage.stale_note") : tr("limits.reset_not_observed")}
                </span>
              )}
            </div>
          ))}
          {r.ageSec != null && <p className="glance-sub muted">{tr("wsbar.usage.fetched", { ago: agoText(r.ageSec) })}</p>}
        </div>
      ))}
    </Tile>
  );
}
