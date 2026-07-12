// WS bar — ported from the old components/WsBar.tsx (docs/22 P6a). Verbatim
// except the useApp() reads, which map onto the zustand stores:
//   wsState/startWs/stopWs       → core/store/workspace
//   tenant/superAdmin            → core/store/tenant
//   layout/splitRight/splitDown/resetToTerminal/activePaneId → layout/store
//   openNewSession               → features/sessions/store (tick signal)
import { useCallback, useEffect, useRef, useState } from "react";
import { api, previewURL } from "../core/api/client.ts";
import { useTenantStore } from "../core/store/tenant.ts";
import { useWorkspaceStore, wsStartBusy } from "../core/store/workspace.ts";
import { useLayoutStore } from "../layout/store.ts";
import { isBlankPane } from "../layout/ops.ts";
import { useSessionsStore } from "../features/sessions/store.ts";
import { Icon } from "../ui/Icon.tsx";
import { Sparkline } from "../ui/Sparkline.tsx";
import { useConfirm } from "../ui/ConfirmProvider.tsx";
import { useIsMobile } from "../lib/device.ts";
import { useDismiss } from "../lib/useDismiss.ts";
import { fmtGiB as fg } from "../lib/bytes.ts";
import { useUsageResetNotify } from "./usageResetNotify.ts";

const HIST_N = 60; // sparkline ring buffer: ~4 min at the 4s poll cadence

interface WsHistPoint {
  cpu: number | null;
  mem: number | null;
}
interface HostHistPoint {
  load: number | null;
  mem: number;
}

// useWsResourceChips polls the workspace + host resource stats every 4s. It lives
// here (not in a global store) on purpose: these values change every tick, so
// holding them in shared state would re-render the whole app (terminals included)
// every 4s and jank/flicker the cursor. Confined to WsBar, only the small bar
// re-renders. Container stats are for everyone; host stats are super_admin-only
// (the CP gates /api/admin/host server-side too).
function useWsResourceChips(tenant: string | null, superAdmin: boolean) {
  const [wsStats, setWsStats] = useState<any>(null);
  const [wsHist, setWsHist] = useState<WsHistPoint[]>([]); // [{cpu, mem}]
  const wsKey = useRef("");
  useEffect(() => {
    let alive = true;
    wsKey.current = "";
    setWsHist([]); // tenant switch → start the trend fresh
    const load = () => {
      if (document.hidden) return; // a backgrounded tab needn't poll (or repaint)
      api("api/workspace/stats")
        .then((d) => {
          if (!alive) return;
          const ok = d && !d.error;
          const running = !!(ok && d.running && d.mem_used != null);
          // Re-render only when a DISPLAYED value changes. memory.current jitters
          // by bytes every read, so keying on the raw stats would repaint the
          // sparkline every 4s even at a steady 0% — which on a remote display
          // flickered the cursor. Key on the rounded mem%/cpu% the chip actually
          // shows, so an idle workspace produces the same key and no re-render.
          const memPct = running && d.mem_max ? Math.round((d.mem_used / d.mem_max) * 100) : -1;
          const cpuPct = running && d.cpu_pct != null ? Math.round(d.cpu_pct) : -1;
          const key = ok ? `${!!(d && d.running)}|${memPct}|${cpuPct}` : "off";
          if (key === wsKey.current) return;
          wsKey.current = key;
          setWsStats(ok ? d : null);
          if (running) {
            const mem = d.mem_max ? d.mem_used / d.mem_max : null;
            const cpu = typeof d.cpu_pct === "number" ? d.cpu_pct : null;
            setWsHist((h) => [...h, { cpu, mem }].slice(-HIST_N));
          } else {
            setWsHist([]); // stopped / unreachable → drop the stale trend
          }
        })
        .catch(() => {
          if (!alive || wsKey.current === "off") return;
          wsKey.current = "off";
          setWsStats(null);
          setWsHist([]);
        });
    };
    load();
    const id = setInterval(load, 4000);
    const onVis = () => !document.hidden && load();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      alive = false;
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [tenant]);

  const [hostStats, setHostStats] = useState<any>(null);
  const [hostHist, setHostHist] = useState<HostHistPoint[]>([]); // [{load, mem}], load normalized to cores
  const hostKey = useRef("");
  useEffect(() => {
    if (!superAdmin) {
      setHostStats(null);
      setHostHist([]);
      return;
    }
    let alive = true;
    hostKey.current = "";
    const load = () => {
      if (document.hidden) return;
      api("api/admin/host")
        .then((d) => {
          if (!alive) return;
          const ok = d && d.mem_total != null;
          // Key on the displayed load (2dp) + rounded mem% so a steady host
          // doesn't repaint the chips every 4s.
          const key = ok ? `${Number(d.load1).toFixed(2)}|${Math.round((d.mem_used / d.mem_total) * 100)}` : "off";
          if (key === hostKey.current) return;
          hostKey.current = key;
          setHostStats(ok ? d : null);
          if (ok) {
            const ldNorm = d.ncpu ? d.load1 / d.ncpu : null;
            const mem = d.mem_used / d.mem_total;
            setHostHist((h) => [...h, { load: ldNorm, mem }].slice(-HIST_N));
          }
        })
        .catch(() => {
          if (!alive || hostKey.current === "off") return;
          hostKey.current = "off";
          setHostStats(null);
        });
    };
    load();
    const id = setInterval(load, 4000);
    const onVis = () => !document.hidden && load();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      alive = false;
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [superAdmin]);

  return { wsStats, wsHist, hostStats, hostHist };
}

// useUsage surfaces one agent's subscription usage (5-hour + weekly) from `endpoint`
// (api/claude/usage or api/codex/usage). The Console reads the Agent's value once on
// mount, on a slow 5-min re-read (to recover the chip if the workspace started after
// mount / stay in sync across tabs), and on an explicit refresh() (?refresh=1) — never
// on a fast background timer (the claude endpoint is unofficial/rate-limited; codex is
// a local rollout read). Kept in WsBar so it doesn't re-render the app.
function useUsage(tenant: string | null, endpoint: string) {
  const [usage, setUsage] = useState<any>(null);
  const [refreshing, setRefreshing] = useState(false);
  const load = useCallback(
    (force: boolean) => {
      if (document.hidden && !force) return;
      if (force) setRefreshing(true);
      api(endpoint + (force ? "?refresh=1" : ""))
        // Keep the last value on a transient error (don't blank); only replace on ok.
        .then((d) => setUsage(d && d.ok ? d : force ? null : (u: any) => u))
        .catch(() => {})
        .finally(() => force && setRefreshing(false));
    },
    [endpoint],
  );
  useEffect(() => {
    setUsage(null);
    load(false);
    const id = setInterval(() => load(false), 300000); // 5 min, Agent-cached (no endpoint hit)
    const onVis = () => !document.hidden && load(false);
    document.addEventListener("visibilitychange", onVis);
    return () => {
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [tenant, load]);
  return { usage, refreshing, refresh: () => load(true) };
}

// agoText: seconds since the usage was fetched → "N秒/分/時間前" (freshness label).
function agoText(sec: number | null | undefined) {
  if (sec == null) return "";
  if (sec >= 3600) return `${Math.round(sec / 3600)}時間前`;
  if (sec >= 60) return `${Math.round(sec / 60)}分前`;
  return `${Math.max(0, sec)}秒前`;
}

// UsageRow: one limit window in the usage dropdown — label + percent, a fill bar, and
// the reset shown both relatively ("あとN時間") and as an absolute date-time.
function UsageRow({ label, w }: { label: string; w: { pct: number; until: string; when: string } }) {
  const level = w.pct >= 95 ? " crit" : w.pct >= 80 ? " warn" : "";
  return (
    <div className="wu-row">
      <div className="wu-row-head">
        <span className="wu-label">{label}</span>
        <span className={"wu-pct" + level}>{w.pct}%</span>
      </div>
      <div className="wu-bar">
        <span className={"wu-bar-fill" + level} style={{ width: Math.min(100, w.pct) + "%" }} />
      </div>
      <div className="wu-reset muted">
        {w.until}でリセット（{w.when}）
      </div>
    </div>
  );
}

// untilText: a reset instant → a compact relative "あとN日/N時間/N分" (the weekday+time
// the app shows is hard to read; relative is glanceable, the absolute date-time goes
// in the tooltip). whenText: the absolute local "M/D HH:MM".
function untilText(iso: string) {
  const t = new Date(iso).getTime();
  if (isNaN(t)) return "";
  const min = Math.max(0, Math.round((t - Date.now()) / 60000));
  if (min >= 1440) return `あと${Math.round(min / 1440)}日`;
  if (min >= 60) return `あと${Math.round(min / 60)}時間`;
  return `あと${min}分`;
}
function whenText(iso: string) {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getMonth() + 1}/${d.getDate()} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// A usage source = one agent's subscription-limit chip (Claude / Codex). Both endpoints
// return the same {ok, fiveHour, sevenDay, ageSec} shape (codex reads its rate_limits
// straight from the rollout — no network), so one chip component renders either.
interface UsageSource {
  endpoint: string;
  name: string; // agent short name ("Claude" / "Codex") — used in reset notifications
  icon: string; // codicon glyph
  cls: string; // kind color class (kind-claude / kind-codex)
  title: string; // chip hover title
  popTitle: string; // dropdown heading
  fiveLabel: string; // 5-hour window label
  weekLabel: string; // weekly window label
  // live = the endpoint queries the current usage (claude), so a 更新 button makes
  // sense. When false (codex), the reading is a snapshot from the last turn — no
  // manual refresh; a note explains it instead.
  live: boolean;
  note?: string;
  // manageURL = the agent vendor's own usage/limits page (opened in a new tab from the
  // dropdown), so the user can jump to the authoritative source for the exact numbers.
  manageURL?: string;
}

const USAGE_SOURCES: UsageSource[] = [
  {
    endpoint: "api/claude/usage",
    name: "Claude",
    icon: "sparkle",
    cls: "kind-claude",
    title: "Claude 使用状況（5時間 / 週次）",
    popTitle: "Claude 使用状況",
    fiveLabel: "5時間制限",
    weekLabel: "週次・全モデル",
    live: true,
    manageURL: "https://claude.ai/new#settings/usage",
  },
  {
    endpoint: "api/codex/usage",
    name: "Codex",
    icon: "rocket",
    cls: "kind-codex",
    title: "Codex 使用状況（5時間 / 週次）",
    popTitle: "Codex 使用状況",
    fiveLabel: "5時間",
    weekLabel: "週次",
    live: false,
    note: "codex が記録した最後の値です（この時点のスナップショット）。次に codex を実行すると更新されます。",
    manageURL: "https://chatgpt.com/#settings/Usage",
  },
];

// UsageChip: a compact per-agent limit chip (glyph + the two window percentages) that
// opens a dropdown with each window's bar + reset + a reload. Renders null until the
// agent's endpoint answers with data, so it self-hides for agents the user doesn't use.
function UsageChip({ src, tenant }: { src: UsageSource; tenant: string | null }) {
  const { usage, refreshing, refresh } = useUsage(tenant, src.endpoint);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  // Notify when a constrained limit window resets (5-hour / weekly). Runs whether or
  // not the dropdown is open — the chip stays mounted while the workspace is up.
  useUsageResetNotify(src, usage, src.fiveLabel, src.weekLabel, refresh);

  const win = (w: { pct: number; resetsAt: string }) => ({
    pct: Math.round(w.pct),
    until: untilText(w.resetsAt),
    when: whenText(w.resetsAt),
  });
  const uh = usage && usage.fiveHour && win(usage.fiveHour);
  const uw = usage && usage.sevenDay && win(usage.sevenDay);
  if (!uh && !uw) return null; // no reading yet → hide the chip entirely
  const label = [uh && `${uh.pct}%`, uw && `${uw.pct}%`].filter(Boolean).join(" / ");

  return (
    <div className="ws-usage-wrap" ref={ref}>
      {/* Same badge look as the Sessions-list kind badge: reuse kind-tag + the kind
          color, then reset the button chrome (ws-usage-btn). */}
      <button
        type="button"
        className={"kind-tag " + src.cls + " ws-usage-btn"}
        title={src.title}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name={src.icon} />
        <span className="ws-usage-nums">{label}</span>
        <Icon name="chevron-down" />
      </button>
      {open && (
        <div className="ws-usage-pop">
          <div className="wu-title">{src.popTitle}</div>
          {uh && <UsageRow label={src.fiveLabel} w={uh} />}
          {uw && <UsageRow label={src.weekLabel} w={uw} />}
          <div className="wu-foot">
            <span className="wu-ago muted">取得 {agoText(usage.ageSec)}</span>
            {src.live && (
              <button type="button" className="ghost wu-reload" onClick={refresh} disabled={refreshing}>
                <Icon name="refresh" spin={refreshing} /> 更新
              </button>
            )}
          </div>
          {!src.live && src.note && <div className="wu-note muted">{src.note}</div>}
          {src.manageURL && (
            <a className="wu-manage" href={src.manageURL} target="_blank" rel="noopener">
              <Icon name="link-external" /> 使用状況ページを開く
            </a>
          )}
        </div>
      )}
    </div>
  );
}

// WS bar: the (single) workspace's state plus Start / Stop. The backend models one
// workspace per membership, so there is no select / create / delete. The
// destructive "作り直す" lives deep in 設定 > 環境 (warning-gated), off this bar.
// On desktop the resource chips / opencode web / port-preview sit inline at the
// right; on a phone they'd wrap to a second line, so they fold into a single ⋯
// overflow popover instead.
// Friendly label for the raw container state. The CP returns runtime-derived
// states (runtime.go State()): "running" | "starting" | "stopped" | "none".
// "starting" is server-reported (ECS: the workspace image cold-pulls for minutes
// before the task runs) — shown as 起動中 and walked to 稼働中 by the 4s poll, no
// reload needed. Stop does `docker rm -f` locally, so the *normal* stopped state
// is "none" (no container — data persists in the bind mount, recreated on Start);
// "stopped" only appears when a container exists but exited on its own (crash /
// OOM) or the ECS service sits at desired 0. Both read as 停止 to the user; the
// raw state stays in the tooltip. Optimistic in-flight states (set by the store
// around start/stop POSTs) end in "…".
function wsLabel(s: string): string {
  switch (s) {
    case "running":
      return "稼働中";
    case "starting":
      return "起動中…";
    case "none":
    case "stopped":
      return "停止";
    case "starting…":
      return "起動中…";
    case "stopping…":
      return "停止中…";
    case "recreating…":
      return "再作成中…";
    case "unknown":
      return "不明";
    default:
      return s; // "…" initial, or any future state
  }
}

// One resource tile: label + trend sparkline + current value, tinted by level
// (0 normal / 1 warn / 2 crit). Called as a plain function (not <Tile/>) so it's
// just inline JSX, no extra component instance.
function tile({
  k,
  series,
  max,
  track,
  value,
  level,
  title,
}: {
  k: string;
  series: (number | null)[];
  max?: number;
  track?: boolean;
  value: string;
  level: number;
  title: string;
}) {
  return (
    <span className={"ws-graph" + (level === 2 ? " crit" : level === 1 ? " warn" : "")} title={title} key={k + title}>
      <span className="ws-graph-k">{k}</span>
      <Sparkline data={series} max={max} track={track} />
      <span className="ws-graph-v">{value}</span>
    </span>
  );
}

export function WsBar() {
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const stopWs = useWorkspaceStore((s) => s.stop);
  const tenant = useTenantStore((s) => s.tenant);
  const superAdmin = useTenantStore((s) => s.superAdmin);
  const layout = useLayoutStore((s) => s.layout);
  const splitRight = useLayoutStore((s) => s.splitRight);
  const splitDown = useLayoutStore((s) => s.splitDown);
  const resetToTerminal = useLayoutStore((s) => s.resetToTerminal);
  const activePaneId = layout.activeId;
  const openNewSession = useSessionsStore((s) => s.openNewSession);
  const askConfirm = useConfirm();
  const { wsStats, wsHist, hostStats, hostHist } = useWsResourceChips(tenant, superAdmin);
  const isMobile = useIsMobile();
  const [port, setPort] = useState("");
  const [pvOpen, setPvOpen] = useState(false); // desktop port-preview popover
  const [moreOpen, setMoreOpen] = useState(false); // mobile overflow popover
  const [resOpen, setResOpen] = useState(false); // desktop resource-tiles popover
  const pvRef = useRef<HTMLDivElement>(null);
  const moreRef = useRef<HTMLDivElement>(null);
  const resRef = useRef<HTMLDivElement>(null);
  const running = wsState === "running";
  // Toggle inert while a transition is in flight: the optimistic "…" states AND the
  // server-reported "starting" (ECS cold pull — a second Start click must not
  // re-drive the deployment; the 4s poll flips the bar to 稼働中 on its own).
  const busy = wsStartBusy(wsState);

  // "Close all panes" collapses the split layout back to one empty terminal pane.
  // Disabled when there's already just a single empty pane (nothing to close).
  const totalPanes = layout.cols.reduce((n, c) => n + c.panes.length, 0);
  const onlyPane = totalPanes === 1 ? layout.cols[0].panes[0] : null;
  const canCloseAll = !(onlyPane && isBlankPane(onlyPane));

  // Split (moved here from the per-pane header): 右に分割 appends a column (global);
  // 上下に分割 splits the ACTIVE pane's column into rows. Same limits as before — up to
  // 4 columns (desktop only), each column up to 2 panes. Mobile: one top/bottom split.
  const activeCol = layout.cols.find((c) => c.panes.some((p) => p.id === activePaneId));
  const canSplitRight = !isMobile && layout.cols.length < 4;
  const canSplitDown = isMobile ? totalPanes < 2 : (activeCol ? activeCol.panes.length : totalPanes) < 2;

  // Start is immediate; Stop is confirmed — it docker-removes the container, so
  // running sessions drop to 停止 (resumable) and opencode web / preview disconnect.
  // Reversible (Start recreates; data persists in the bind mount), so it's a caution,
  // not a red destructive action.
  const onToggle = async () => {
    if (!running) {
      void startWs();
      return;
    }
    const ok = await askConfirm({
      title: "ワークスペースを停止",
      body: "コンテナを停止します。実行中のセッションは停止（あとで再開可）になり、opencode web / プレビューは切断されます。ファイルは保持されます。",
      confirmLabel: "停止する",
      danger: false,
    });
    if (ok) void stopWs();
  };

  // Open a service the user started inside the container (e.g. a Spring Boot app
  // on :8080) in a new tab, proxied through the CP /preview/{port}.
  const openPreview = () => {
    const p = port.trim();
    if (!p) return;
    window.open(previewURL(p), "_blank", "noopener");
    setPvOpen(false);
    setMoreOpen(false);
  };

  // Close each popover on an outside click / Escape.
  useDismiss(pvRef, pvOpen, () => setPvOpen(false));
  useDismiss(moreRef, moreOpen, () => setMoreOpen(false));
  useDismiss(resRef, resOpen, () => setResOpen(false));

  // --- pieces shared between the inline (desktop) and folded (mobile) layouts ---
  const lvl = (v: number | null, warn: number, crit: number) => (v == null ? 0 : v >= crit ? 2 : v >= warn ? 1 : 0);

  // Container (own workspace): memory fill (vs quota) + CPU%. Shown to everyone.
  const hasWs = wsStats && wsStats.running && wsStats.mem_used != null;
  const memRatio = hasWs && wsStats.mem_max ? wsStats.mem_used / wsStats.mem_max : null;
  const containerTiles = hasWs && (
    <>
      {tile({
        k: "mem",
        series: wsHist.map((p) => p.mem),
        max: 1,
        track: true,
        value: memRatio != null ? `${Math.round(memRatio * 100)}%` : `${fg(wsStats.mem_used)}G`,
        level: lvl(memRatio, 0.75, 0.9),
        title: `ワークスペースのメモリ: ${fg(wsStats.mem_used)}${wsStats.mem_max ? "/" + fg(wsStats.mem_max) : ""}G`,
      })}
      {tile({
        k: "cpu",
        series: wsHist.map((p) => p.cpu),
        value: wsStats.cpu_pct != null ? `${Math.round(wsStats.cpu_pct)}%` : "–",
        level: lvl(wsStats.cpu_pct, 60, 90),
        title: "ワークスペースの CPU 使用率（1コア = 100%）",
      })}
    </>
  );

  // Host (super_admin only): load average (normalized to cores) + total memory fill.
  const hasHost = hostStats && hostStats.mem_total != null;
  const loadNorm = hasHost && hostStats.ncpu ? hostStats.load1 / hostStats.ncpu : null;
  const hostMemRatio = hasHost ? hostStats.mem_used / hostStats.mem_total : null;
  const hostTiles = hasHost && (
    <>
      {tile({
        k: "ld",
        series: hostHist.map((p) => p.load),
        max: 1,
        track: true,
        value: Number(hostStats.load1).toFixed(2),
        level: lvl(loadNorm, 0.7, 1),
        title: `ホスト ロードアベレージ(1分): ${Number(hostStats.load1).toFixed(2)} / ${hostStats.ncpu}コア（管理者のみ）`,
      })}
      {tile({
        k: "mem",
        series: hostHist.map((p) => p.mem),
        max: 1,
        track: true,
        value: hostMemRatio != null ? `${Math.round(hostMemRatio * 100)}%` : "",
        level: lvl(hostMemRatio, 0.8, 0.92),
        title: `ホスト メモリ: ${fg(hostStats.mem_used)}/${fg(hostStats.mem_total)}G（管理者のみ）`,
      })}
    </>
  );

  const graphs = (containerTiles || hostTiles) && (
    <>
      {containerTiles}
      {containerTiles && hostTiles && <span className="ws-graph-sep" />}
      {hostTiles}
    </>
  );

  // Desktop: collapse the resource sparkline tiles behind one "リソース" chip so the
  // bar isn't a wall of tiles. The chip shows a glanceable container summary
  // (mem/cpu %) and opens a popover with the full trend tiles (incl. host for
  // super_admin). On mobile the tiles already live in the ⋯ overflow, so this is
  // desktop-only (graphs is reused there).
  const resSummary = hasWs
    ? `mem ${memRatio != null ? Math.round(memRatio * 100) : "–"}% · cpu ${
        wsStats.cpu_pct != null ? Math.round(wsStats.cpu_pct) : "–"
      }%`
    : null;
  const resourcesEl = graphs && (
    <div className="ws-res" ref={resRef}>
      <button
        type="button"
        className="ghost ws-res-btn"
        title="リソース使用状況"
        aria-expanded={resOpen}
        onClick={() => setResOpen((o) => !o)}
      >
        <Icon name="pulse" />
        <span className="ws-res-sum">{resSummary || "リソース"}</span>
        <Icon name="chevron-down" />
      </button>
      {resOpen && <div className="ws-res-pop">{graphs}</div>}
    </div>
  );

  // Subscription-usage chips, one per agent that exposes limits (Claude, Codex). Each
  // self-hides until its endpoint answers, so a claude-only user sees one chip, a user
  // of both sees two. The 8px bar gap spaces them (no manual dividers needed).
  const usageChips = USAGE_SOURCES.map((s) => <UsageChip key={s.endpoint} src={s} tenant={tenant} />);

  const statsBlock = (
    <>
      {graphs}
      {usageChips}
    </>
  );

  const previewPop = (
    <>
      <div className="pv-section">
        <label className="pv-label">ポートを指定して開く</label>
        <div className="pv-row">
          <input
            className="preview-port"
            type="number"
            min="1"
            max="65535"
            placeholder="port"
            value={port}
            onChange={(e) => setPort(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && openPreview()}
            title="コンテナ内で起動したサービスのポート（例: 8080）"
          />
          <button onClick={openPreview} disabled={!port.trim()}>
            開く
          </button>
        </div>
        <div className="pv-hint">コンテナ内で起動したサービスのポート（例: 8080）を新しいタブで開きます。</div>
      </div>
    </>
  );

  return (
    <div className="wsbar">
      <span className="ws-label">Workspace</span>
      {/* Power toggle: a single icon-only ⏻ to the LEFT of the state chip — the chip
          says 稼働中/停止, this starts or stops accordingly (no separate labeled
          button). The bar auto-syncs wsState from the 4s workspace poll, so an
          externally-changed workspace (admin stop / OOM) reflects without a manual
          refresh. Disabled mid-transition (starting…/stopping…), where it shows a
          spinner instead of the glyph. */}
      <button
        className={"ws-power " + (running ? "on" : "off")}
        onClick={onToggle}
        disabled={busy}
        title={running ? "ワークスペースを停止" : "ワークスペースを起動"}
        aria-label={running ? "ワークスペースを停止" : "ワークスペースを起動"}
      >
        {busy ? (
          <Icon name="loading" spin />
        ) : (
          // Inline SVG power symbol — the Unicode ⏻ (U+23FB) is missing from many
          // mobile system fonts (renders blank on phones), so draw it instead. Inherits
          // currentColor, so the state/hover color classes still apply.
          <svg
            className="ws-power-glyph"
            viewBox="0 0 24 24"
            width="15"
            height="15"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <path d="M12 3.5v8" />
            <path d="M7.3 6.7a7 7 0 1 0 9.4 0" />
          </svg>
        )}
      </button>
      <span className={"ws-dot " + (running ? "on" : "off")}>●</span>
      <span
        className="ws-state"
        title={
          wsState === "none"
            ? "停止（コンテナなし — Stop で削除済み。データは保持、Start で再作成）"
            : wsState === "stopped"
              ? "停止（コンテナが自走終了 — クラッシュ / OOM の可能性）"
              : wsState === "starting"
                ? "起動中（初回はイメージ取得のため数分かかることがあります。完了すると自動で稼働中になります）"
                : `状態: ${wsState}`
        }
      >
        {wsLabel(wsState)}
      </span>
      {/* Second entry point to the New Session dialog (the Sessions-list ＋新規 stays as
          is): handy when the left pane is scrolled / collapsed. Opens the same global
          dialog via openNewSession; disabled while the workspace is stopped. */}
      <button
        className="ghost ws-split ws-newsession"
        title={running ? "新規セッション" : "新規セッション（ワークスペース停止中）"}
        disabled={!running}
        onClick={openNewSession}
      >
        <Icon name="add" />
        <span className="lbl">新規</span>
      </button>
      <button
        className="ghost ws-split"
        title="右に分割"
        disabled={!canSplitRight}
        onClick={() => splitRight()}
      >
        <Icon name="split-horizontal" />
        <span className="lbl">右に分割</span>
      </button>
      <button
        className="ghost ws-split"
        title="上下に分割（アクティブなペイン）"
        disabled={!canSplitDown}
        onClick={() => activePaneId && splitDown(activePaneId)}
      >
        <Icon name="split-vertical" />
        <span className="lbl">下に分割</span>
      </button>
      <button
        className="ghost ws-closeall"
        title="全ペインを閉じる"
        disabled={!canCloseAll}
        onClick={() => resetToTerminal()}
      >
        <Icon name="close-all" />
        <span className="lbl">全て閉じる</span>
      </button>

      <span className="ws-spacer" />

      {isMobile ? (
        <div className="ws-more" ref={moreRef}>
          <button
            className="ghost ws-more-btn"
            title="リソース情報 / opencode web / プレビュー"
            onClick={() => setMoreOpen((o) => !o)}
          >
            <Icon name="ellipsis" />
          </button>
          {moreOpen && (
            <div className="ws-more-pop">
              {statsBlock && <div className="ws-more-stats">{statsBlock}</div>}
              {previewPop}
            </div>
          )}
        </div>
      ) : (
        <>
          {usageChips}
          {resourcesEl}
          <div className="ws-preview" ref={pvRef}>
            <button
              className="ghost ws-preview-btn"
              disabled={!running}
              title="opencode web / コンテナ内サービスを開く"
              aria-expanded={pvOpen}
              onClick={() => setPvOpen((o) => !o)}
            >
              <Icon name="globe" /> プレビュー <Icon name="chevron-down" />
            </button>
            {pvOpen && <div className="ws-preview-pop">{previewPop}</div>}
          </div>
        </>
      )}
    </div>
  );
}
