// WS bar — ported from the old components/WsBar.tsx (docs/22 P6a). Verbatim
// except the useApp() reads, which map onto the zustand stores:
//   wsState/startWs/stopWs       → core/store/workspace
//   tenant/superAdmin            → core/store/tenant
//   layout/splitRight/splitDown/resetToTerminal/activePaneId → layout/store
//   openNewSession               → features/sessions/store (tick signal)
import { useCallback, useEffect, useRef, useState } from "react";
import { api, previewURL } from "../core/api/client.ts";
import { onPush, pushHealthy } from "../core/push/events.ts";
import { useTenantStore } from "../core/store/tenant.ts";
import { useWorkspaceStore, wsStartBusy } from "../core/store/workspace.ts";
import { useLayoutStore } from "../layout/store.ts";
import { isBlankPane, MAX_TAB_COLS } from "../layout/ops.ts";
import { useSessionsStore } from "../features/sessions/store.ts";
import { hintSuffix } from "../features/keys/keyHint.ts";
import { Icon } from "../ui/Icon.tsx";
import { Sparkline } from "../ui/Sparkline.tsx";
import { useConfirm } from "../ui/ConfirmProvider.tsx";
import { useIsMobile } from "../lib/device.ts";
import { useDismiss } from "../lib/useDismiss.ts";
import { listBrowserAttachments } from "../features/browser/attachmentService.ts";
import { openBrowserAttachment } from "../features/browser/attachmentAction.ts";
import type { BrowserAttachmentStatus } from "../features/browser/attachmentController.ts";
import { useOpenSignal, type OpenTarget } from "../core/store/uiOpen.ts";
import { fmtDateTime, TIME_HM } from "../lib/intl.ts";
import { t, tCount, useT } from "../lib/i18n/index.ts";
import type { MsgKey } from "../lib/i18n/index.ts";
import { fmtGiB as fg } from "../lib/bytes.ts";
import { useUsageResetNotify } from "./usageResetNotify.ts";
import { useSettingsUI } from "../features/settings/store.ts";
import { browserTarget } from "../features/browser/target.ts";

const HIST_N = 60; // sparkline ring buffer: ~4 min at the 4s poll cadence

interface WsHistPoint {
  cpu: number | null;
  mem: number | null;
}
interface HostHistPoint {
  load: number | null;
  mem: number;
}

// api/workspace/stats・api/admin/host 応答のうち WS バーが表示に使うフィールドだけの
// 部分型（応答全体の正は Agent/CP 側 — 未知フィールドは素通し）。
interface WsStats {
  running?: boolean;
  mem_used?: number | null;
  mem_max?: number | null;
  cpu_pct?: number | null;
  oom_recent?: boolean;
  oom_killed?: boolean;
  exit_code?: number | null;
}
interface HostStats {
  mem_total: number;
  mem_used: number;
  load1: number;
  ncpu?: number;
}

// 使用量チップ各種（claude/codex/agy/copilot）が共有する reading の部分型。エージェント毎に
// 使うフィールドは異なる（fiveHour/sevenDay ↔ groups ↔ quotas）が、shape は 1 つの union に
// せず optional の合併で表す — チップ側は元々フィールドの有無で分岐している。
interface UsageWindow {
  pct: number;
  resetsAt: string;
}
interface UsageReading {
  ok?: boolean;
  authed?: boolean;
  ageSec?: number;
  user?: string;
  plan?: string;
  planType?: string;
  account?: string;
  fiveHour?: UsageWindow;
  sevenDay?: UsageWindow;
  resetCredits?: { availableCount?: number; credits?: { expiresAt?: string }[] };
  // agy: モデルグループ毎のプール（週次＋有償tierは5時間枠）。remaining ベース。
  groups?: {
    label: string;
    remainingPct: number;
    resetsAt?: string;
    fiveHour?: { remainingPct: number; resetsAt?: string };
  }[];
  // copilot: プール毎のクォータ＋共通の月次リセット。
  quotas?: { id: string; remainingPct: number }[];
  resetsAt?: string;
  canUpgrade?: boolean;
}

// useWsResourceChips polls the workspace + host resource stats every 4s. It lives
// here (not in a global store) on purpose: these values change every tick, so
// holding them in shared state would re-render the whole app (terminals included)
// every 4s and jank/flicker the cursor. Confined to WsBar, only the small bar
// re-renders. Container stats are for everyone; host stats are super_admin-only
// (the CP gates /api/admin/host server-side too).
function useWsResourceChips(tenant: string | null, superAdmin: boolean) {
  const [wsStats, setWsStats] = useState<WsStats | null>(null);
  const [wsHist, setWsHist] = useState<WsHistPoint[]>([]); // [{cpu, mem}]
  const wsKey = useRef("");
  useEffect(() => {
    let alive = true;
    wsKey.current = "";
    setWsHist([]); // tenant switch → start the trend fresh
    // apply adopts one stats payload — from the 4s poll or a pushed api/events
    // frame (identical shape).
    const apply = (d: any) => {
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
      // OOM flags are part of the key so a fresh in-container OOM (oom_recent) or a
      // stopped-from-OOM container (oom_killed) repaints even at a steady mem/cpu.
      const oom = `${d.oom_recent ? 1 : 0}|${d.oom_killed ? 1 : 0}|${d.exit_code ?? ""}`;
      const key = ok ? `${!!(d && d.running)}|${memPct}|${cpuPct}|${oom}` : "off";
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
    };
    const load = () => {
      // a backgrounded tab needn't poll (or repaint); while the push channel is
      // live the stats frames arrive on their own (the poll is the fallback)
      if (document.hidden || pushHealthy()) return;
      api("api/workspace/stats")
        .then(apply)
        .catch(() => {
          if (!alive || wsKey.current === "off") return;
          wsKey.current = "off";
          setWsStats(null);
          setWsHist([]);
        });
    };
    const unPush = onPush("stats", apply);
    load();
    const id = setInterval(load, 4000);
    const onVis = () => !document.hidden && load();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      alive = false;
      unPush();
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [tenant]);

  const [hostStats, setHostStats] = useState<HostStats | null>(null);
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
  const [usage, setUsage] = useState<UsageReading | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const load = useCallback(
    (force: boolean) => {
      if (document.hidden && !force) return;
      if (force) setRefreshing(true);
      api(endpoint + (force ? "?refresh=1" : ""))
        // Prefer a fresh ok reading. On a failed read keep the last good value (don't
        // blank a working chip), whether it was a background poll or a manual refresh —
        // but if we have nothing yet, still adopt the failed payload so its `authed` flag
        // can render the degraded "unavailable" chip instead of hiding entirely.
        .then((d) => setUsage((u) => (d && (d.ok || !u) ? d : u)))
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
  if (sec >= 3600) return tCount("wsbar.ago_hour", Math.round(sec / 3600));
  if (sec >= 60) return tCount("wsbar.ago_min", Math.round(sec / 60));
  return tCount("wsbar.ago_sec", Math.max(0, sec));
}

// UsageBreakdownLink: 使用量チップ → 設定「使用量」タブへのディープリンク（docs/46 §5）。
// このチップが答えるのは「サブスク枠がどれだけ残っているか」で、「何にトークンを使ったか」は
// 別の問い。枠を見て「で、何に消えた?」となった所からそのまま渡す導線。
function UsageBreakdownLink({ onNavigate }: { onNavigate: () => void }) {
  const tr = useT();
  const openSettings = useSettingsUI((s) => s.openSettings);
  return (
    <button
      type="button"
      className="wu-manage"
      onClick={() => {
        onNavigate();
        openSettings("usage");
      }}
    >
      <Icon name="graph" /> {tr("wsbar.usage.breakdown")}
    </button>
  );
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
      <div className="wu-reset muted">{t("wsbar.usage.reset_at", { until: w.until, when: w.when })}</div>
    </div>
  );
}

// untilText: a reset instant → a compact relative "あとN日/N時間/N分" (the weekday+time
// the app shows is hard to read; relative is glanceable, the absolute date-time goes
// in the tooltip). whenText: the absolute local "M/D HH:MM".
function untilText(iso: string) {
  const ms = new Date(iso).getTime();
  if (isNaN(ms)) return "";
  const min = Math.max(0, Math.round((ms - Date.now()) / 60000));
  if (min >= 1440) return tCount("common.days_left", Math.round(min / 1440));
  if (min >= 60) return tCount("wsbar.until_hour", Math.round(min / 60));
  return tCount("wsbar.until_min", min);
}
const whenText = (iso: string) => fmtDateTime(iso);

function expiryText(iso: string) {
  const ms = new Date(iso).getTime();
  if (isNaN(ms)) return "";
  const days = Math.ceil((ms - Date.now()) / 86400000);
  if (days <= 0) return t("wsbar.expiry_today");
  return days === 1 ? t("wsbar.expiry_tomorrow") : tCount("common.days_left", days);
}

// resetChipText: reset instant → the compact form shown ON the chip when a window is
// Max付近. Same-day resets drop the date (just HH:MM); a later day keeps M/D so the
// day is unambiguous.
function resetChipText(iso: string) {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const now = new Date();
  const sameDay =
    d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
  return sameDay ? fmtDateTime(d, TIME_HM) : fmtDateTime(d);
}

// Max付近しきい値: 枠の利用率がこの値以上なら、チップを「N% / M%」からその枠のリセット時刻
// 表示へ切り替える（あと僅かで詰まる／既に詰まっている＝「いつ解放されるか」の方が有用）。
// ドロップダウンの crit 着色境界（95%）と揃える。
const NEAR_MAX_PCT = 95;

// A usage source = one agent's subscription-limit chip (Claude / Codex). Both endpoints
// return the same {ok, fiveHour, sevenDay, ageSec} shape (codex reads its rate_limits
// straight from the rollout — no network), so one chip component renders either.
interface UsageSource {
  endpoint: string;
  key: OpenTarget; // keyboard-open target id (uiOpen signal)
  name: string; // agent short name ("Claude" / "Codex") — used in reset notifications
  icon: string; // codicon glyph
  cls: string; // kind color class (kind-claude / kind-codex)
  fiveLabelKey: MsgKey; // 5-hour window label (i18n key)
  weekLabelKey: MsgKey; // weekly window label (i18n key)
  // live = the endpoint queries the current usage (claude), so a 更新 button makes
  // sense. When false (codex), the reading is a snapshot from the last turn — no
  // manual refresh; a note explains it instead.
  live: boolean;
  noteKey?: MsgKey;
  // manageURL = the agent vendor's own usage/limits page (opened in a new tab from the
  // dropdown), so the user can jump to the authoritative source for the exact numbers.
  manageURL?: string;
}

const USAGE_SOURCES: UsageSource[] = [
  {
    endpoint: "api/claude/usage",
    key: "usage-claude",
    name: "Claude",
    icon: "sparkle",
    cls: "kind-claude",
    fiveLabelKey: "wsbar.usage.claude.five",
    weekLabelKey: "wsbar.usage.claude.week",
    live: true,
    manageURL: "https://claude.ai/new#settings/usage",
  },
  {
    endpoint: "api/codex/usage",
    key: "usage-codex",
    name: "Codex",
    icon: "rocket",
    cls: "kind-codex",
    fiveLabelKey: "wsbar.usage.codex.five",
    weekLabelKey: "wsbar.usage.codex.week",
    live: false,
    noteKey: "wsbar.usage.codex.note",
    manageURL: "https://chatgpt.com/#settings/Usage",
  },
];

// UsageChip: a compact per-agent limit chip (glyph + the two window percentages) that
// opens a dropdown with each window's bar + reset + a reload. Renders null until the
// agent's endpoint answers with data, so it self-hides for agents the user doesn't use.
function UsageChip({ src, tenant }: { src: UsageSource; tenant: string | null }) {
  const tr = useT();
  const fiveLabel = tr(src.fiveLabelKey);
  const weekLabel = tr(src.weekLabelKey);
  const { usage, refreshing, refresh } = useUsage(tenant, src.endpoint);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  // Keyboard: Ctrl/⌘+K g c / g x toggles this agent's usage popover.
  useOpenSignal(src.key, () => setOpen((o) => !o));
  // Notify when a constrained limit window resets (5-hour / weekly). Runs whether or
  // not the dropdown is open — the chip stays mounted while the workspace is up.
  useUsageResetNotify(src, usage, refresh);

  const win = (w: { pct: number; resetsAt: string }) => ({
    pct: Math.round(w.pct),
    until: untilText(w.resetsAt),
    when: whenText(w.resetsAt),
  });
  const uh = usage && usage.fiveHour && win(usage.fiveHour);
  const uw = usage && usage.sevenDay && win(usage.sevenDay);
  const resetCount = usage?.resetCredits?.availableCount || 0;
  const fullResets = (usage?.resetCredits?.credits || [])
    .filter((r): r is { expiresAt: string } => !!r?.expiresAt && !isNaN(new Date(r.expiresAt).getTime()))
    .sort((a, b) => new Date(a.expiresAt).getTime() - new Date(b.expiresAt).getTime());
  // Hide the chip only when the user isn't signed into this agent (nothing to show, and
  // never will be). If they ARE signed in but the (unofficial, rate-limited) reading is
  // momentarily unavailable, keep a degraded chip so it never vanishes on a transient
  // failure — its dropdown links out to the vendor's own usage page to check manually.
  const unavailable = !uh && !uw && !resetCount;
  if (unavailable && !usage?.authed) return null;

  // Max付近（利用率 ≥ NEAR_MAX_PCT）の枠があれば、% ではなく「いつ解放されるか」を出す。候補は
  // Max付近の枠だけ、その中で最も早くリセットする枠（＝最初に解放される時刻）を 1 つだけ。両枠
  // とも Max付近なら近い方（通常は5時間枠）になる。詳細（どの枠か・%・相対）はツールチップへ。
  const nearMax: { label: string; pct: number; resetsAt: string }[] = [];
  if (usage?.fiveHour && usage.fiveHour.pct >= NEAR_MAX_PCT && usage.fiveHour.resetsAt)
    nearMax.push({ label: fiveLabel, ...usage.fiveHour });
  if (usage?.sevenDay && usage.sevenDay.pct >= NEAR_MAX_PCT && usage.sevenDay.resetsAt)
    nearMax.push({ label: weekLabel, ...usage.sevenDay });
  const bind = nearMax.length
    ? nearMax.reduce((a, b) => (new Date(a.resetsAt).getTime() <= new Date(b.resetsAt).getTime() ? a : b))
    : null;

  const label = unavailable
    ? "—"
    : bind
      ? resetChipText(bind.resetsAt)
      : [uh && `${uh.pct}%`, uw && `${uw.pct}%`].filter(Boolean).join(" / ");
  const chipTitle = unavailable
    ? tr("wsbar.usage.unavailable_title", { name: src.name })
    : bind
      ? tr("wsbar.usage.chip_bind_title", {
          name: src.name,
          label: bind.label,
          pct: Math.round(bind.pct),
          until: untilText(bind.resetsAt),
          when: whenText(bind.resetsAt),
        })
      : tr("wsbar.usage.title", { name: src.name });

  return (
    <div className="ws-usage-wrap" ref={ref}>
      {/* Same badge look as the Sessions-list kind badge: reuse kind-tag + the kind
          color, then reset the button chrome (ws-usage-btn). */}
      <button
        type="button"
        className={"kind-tag " + src.cls + " ws-usage-btn"}
        title={chipTitle}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name={src.icon} />
        <span className={"ws-usage-nums" + (bind ? " crit" : unavailable ? " muted" : "")}>{label}</span>
        {!!resetCount && (
          <span className={"ws-reset-count" + (fullResets.length && new Date(fullResets[0].expiresAt).getTime() - Date.now() <= 7 * 86400000 ? " warn" : "")}
            title={tCount("wsbar.usage.full_reset", resetCount) + (fullResets.length ? tr("wsbar.usage.full_reset_soonest", { when: whenText(fullResets[0].expiresAt) }) : "")}>
            +{resetCount}
          </span>
        )}
        <Icon name="chevron-down" />
      </button>
      {open && (
        <div className="ws-usage-pop">
          <div className="wu-title">{tr("wsbar.usage.pop_title", { name: src.name })}</div>
          {/* Account + subscription tier: claude's HandleUsage returns `user`/`plan`,
              codex returns `user`/`planType` — surface whichever is present. */}
          {usage?.user && <div className="wu-note muted">{tr("wsbar.usage.user", { user: usage.user })}</div>}
          {(usage?.planType || usage?.plan) && (
            <div className="wu-note muted">{tr("wsbar.usage.plan", { plan: usage?.planType || usage?.plan || "" })}</div>
          )}
          {unavailable ? (
            <div className="wu-note muted">{tr("wsbar.usage.unavailable_note", { name: src.name })}</div>
          ) : (
            <>
              {uh && <UsageRow label={fiveLabel} w={uh} />}
              {uw && <UsageRow label={weekLabel} w={uw} />}
              {!!resetCount && (
                <div className="wu-full-resets">
                  <div className="wu-row-head"><span className="wu-label">{tr("wsbar.usage.full_reset_label")}</span><span>{tr("wsbar.usage.count_ken", { count: resetCount })}</span></div>
                  {fullResets.map((reset, i) => (
                    <div className="wu-expiry" key={`${reset.expiresAt}-${i}`}>
                      <span>{tr("wsbar.usage.expires", { date: whenText(reset.expiresAt).split(" ")[0] })}</span>
                      <span className={new Date(reset.expiresAt).getTime() - Date.now() <= 7 * 86400000 ? "warn" : "muted"}>{expiryText(reset.expiresAt)}</span>
                    </div>
                  ))}
                </div>
              )}
            </>
          )}
          <div className="wu-foot">
            {!unavailable && <span className="wu-ago muted">{tr("wsbar.usage.fetched", { ago: agoText(usage?.ageSec) })}</span>}
            {src.live && (
              <button type="button" className="ghost wu-reload" onClick={refresh} disabled={refreshing}>
                <Icon name="refresh" spin={refreshing} /> {tr("wsbar.usage.refresh")}
              </button>
            )}
          </div>
          {!unavailable && !src.live && src.noteKey && <div className="wu-note muted">{tr(src.noteKey)}</div>}
          <UsageBreakdownLink onNavigate={() => setOpen(false)} />
          {src.manageURL && (
            <a className="wu-manage" href={src.manageURL} target="_blank" rel="noopener">
              <Icon name="link-external" /> {tr("wsbar.usage.open_page")}
            </a>
          )}
        </div>
      )}
    </div>
  );
}

// AgyUsageChip: Antigravity's quota chip (docs/32 — moved here from the AgyCard so
// the 残量 sits beside the Claude / Codex chips). agy's wallet is split into
// per-model-group pools (Gemini / Claude+GPT), each with a weekly window plus a
// 5-hour window on paid tiers, and the agent reports REMAINING percent (matching
// the TUI's own /usage bars). Shown as USED percent so the chip and dropdown read
// exactly like the other agents'. The chip numbers are the first group's (Gemini —
// agy's default-model pool); the dropdown lists every group's windows. Self-hides
// while agy isn't signed in (authed:false), same rule as the generic chip.
function AgyUsageChip({ tenant }: { tenant: string | null }) {
  const tr = useT();
  const { usage, refreshing, refresh } = useUsage(tenant, "api/connections/agy/usage");
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  // Keyboard: Ctrl/⌘+K g a toggles this popover (g c / g x = Claude / Codex).
  useOpenSignal("usage-agy", () => setOpen((o) => !o));

  // Flatten groups into rows: per group the weekly window, then the 5h one.
  const used = (remainingPct: number) => Math.round(Math.min(100, Math.max(0, 100 - remainingPct)));
  const wins: { label: string; pct: number; resetsAt?: string }[] = [];
  for (const g of (usage?.ok && Array.isArray(usage.groups) && usage.groups) || []) {
    wins.push({ label: tr("wsbar.usage.agy.week_row", { group: g.label }), pct: used(g.remainingPct), resetsAt: g.resetsAt });
    if (g.fiveHour)
      wins.push({
        label: tr("wsbar.usage.agy.five_row", { group: g.label }),
        pct: used(g.fiveHour.remainingPct),
        resetsAt: g.fiveHour.resetsAt,
      });
  }
  const unavailable = wins.length === 0;
  if (unavailable && !usage?.authed) return null;

  // Max付近: any window ≥ NEAR_MAX_PCT switches the chip to the earliest reset time
  // among the constrained windows (same rule as UsageChip).
  const nearMax = wins.filter((w) => w.pct >= NEAR_MAX_PCT && w.resetsAt);
  const bind = nearMax.length
    ? nearMax.reduce((a, b) => (new Date(a.resetsAt!).getTime() <= new Date(b.resetsAt!).getTime() ? a : b))
    : null;

  // Chip numbers: the first group (Gemini), 5h% / weekly% — same order as the other chips.
  const g0 = usage?.ok && Array.isArray(usage.groups) ? usage.groups[0] : null;
  const label = unavailable
    ? "—"
    : bind
      ? resetChipText(bind.resetsAt!)
      : [g0?.fiveHour && `${used(g0.fiveHour.remainingPct)}%`, g0 && `${used(g0.remainingPct)}%`]
          .filter(Boolean)
          .join(" / ");
  const chipTitle = unavailable
    ? tr("wsbar.usage.unavailable_title", { name: "Antigravity" })
    : bind
      ? tr("wsbar.usage.chip_bind_title", {
          name: "Antigravity",
          label: bind.label,
          pct: bind.pct,
          until: untilText(bind.resetsAt!),
          when: whenText(bind.resetsAt!),
        })
      : tr("wsbar.usage.title", { name: "Antigravity" });

  return (
    <div className="ws-usage-wrap" ref={ref}>
      <button
        type="button"
        className="kind-tag kind-agy ws-usage-btn"
        title={chipTitle}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name="magnet" />
        <span className={"ws-usage-nums" + (bind ? " crit" : unavailable ? " muted" : "")}>{label}</span>
        <Icon name="chevron-down" />
      </button>
      {open && (
        <div className="ws-usage-pop">
          <div className="wu-title">{tr("wsbar.usage.pop_title", { name: "Antigravity" })}</div>
          {usage?.account && <div className="wu-note muted">{tr("wsbar.usage.user", { user: usage.account })}</div>}
          {usage?.plan && <div className="wu-note muted">{tr("wsbar.usage.plan", { plan: usage.plan })}</div>}
          {unavailable ? (
            <div className="wu-note muted">{tr("wsbar.usage.unavailable_note", { name: "Antigravity" })}</div>
          ) : (
            wins.map((w) => (
              <UsageRow
                key={w.label}
                label={w.label}
                w={{ pct: w.pct, until: w.resetsAt ? untilText(w.resetsAt) : "", when: w.resetsAt ? whenText(w.resetsAt) : "" }}
              />
            ))
          )}
          {/* 実験枠 note (採用条件 — docs/32 Track C-3): the Starter pool is tiny and
              shared with the Antigravity IDE / Jules, so the popover always says so. */}
          <div className="wu-note muted">{tr("agents.agy_exp_label")}</div>
          <div className="wu-foot">
            {!unavailable && <span className="wu-ago muted">{tr("wsbar.usage.fetched", { ago: agoText(usage?.ageSec) })}</span>}
            <button type="button" className="ghost wu-reload" onClick={refresh} disabled={refreshing}>
              <Icon name="refresh" spin={refreshing} /> {tr("wsbar.usage.refresh")}
            </button>
          </div>
          <UsageBreakdownLink onNavigate={() => setOpen(false)} />
        </div>
      )}
    </div>
  );
}

// CopilotUsageChip: GitHub Copilot's account credit chip. Unlike agy this needs no
// TUI scrape — the backend reads copilot_internal/user (gh transparent auth) and
// returns structured {plan, sku, resetsAt, quotas:[{id, remainingPct, ...}]}. Each
// quota pool (chat / completions / premium_interactions, plan-dependent) shares the
// one monthly reset date. The plan is shown in the popover (the user asked to see it).
function CopilotUsageChip({ tenant }: { tenant: string | null }) {
  const tr = useT();
  const { usage, refreshing, refresh } = useUsage(tenant, "api/copilot/usage");
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  useOpenSignal("usage-copilot", () => setOpen((o) => !o));

  const used = (remainingPct: number) => Math.round(Math.min(100, Math.max(0, 100 - remainingPct)));
  // Localized pool names; unknown ids fall back to the raw id.
  const poolLabel = (id: string) =>
    id === "chat"
      ? tr("wsbar.usage.copilot.pool_chat")
      : id === "completions"
        ? tr("wsbar.usage.copilot.pool_completions")
        : id === "premium_interactions"
          ? tr("wsbar.usage.copilot.pool_premium")
          : id;
  const quotas: { id: string; remainingPct: number }[] =
    (usage?.ok && Array.isArray(usage.quotas) && usage.quotas) || [];
  const resetsAt: string | undefined = usage?.resetsAt;
  const wins = quotas.map((q) => ({ label: poolLabel(q.id), pct: used(q.remainingPct), resetsAt }));
  const unavailable = wins.length === 0;
  if (unavailable && !usage?.authed) return null;

  const nearMax = wins.filter((w) => w.pct >= NEAR_MAX_PCT && w.resetsAt);
  const bind = nearMax.length
    ? nearMax.reduce((a, b) => (new Date(a.resetsAt!).getTime() <= new Date(b.resetsAt!).getTime() ? a : b))
    : null;

  // Chip number: the primary pool's used% (quotas are pre-ordered by the backend —
  // premium first on paid plans, else chat).
  const label = unavailable ? "—" : bind ? resetChipText(bind.resetsAt!) : `${wins[0].pct}%`;
  const chipTitle = unavailable
    ? tr("wsbar.usage.unavailable_title", { name: "GitHub Copilot" })
    : bind
      ? tr("wsbar.usage.chip_bind_title", {
          name: "GitHub Copilot",
          label: bind.label,
          pct: bind.pct,
          until: untilText(bind.resetsAt!),
          when: whenText(bind.resetsAt!),
        })
      : tr("wsbar.usage.copilot.title");
  const plan: string = usage?.plan || "";

  return (
    <div className="ws-usage-wrap" ref={ref}>
      <button
        type="button"
        className="kind-tag kind-copilot ws-usage-btn"
        title={chipTitle}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name="copilot" />
        <span className={"ws-usage-nums" + (bind ? " crit" : unavailable ? " muted" : "")}>{label}</span>
        <Icon name="chevron-down" />
      </button>
      {open && (
        <div className="ws-usage-pop">
          <div className="wu-title">{tr("wsbar.usage.pop_title", { name: "GitHub Copilot" })}</div>
          {usage?.user && <div className="wu-note muted">{tr("wsbar.usage.user", { user: usage.user })}</div>}
          {plan && (
            <div className="wu-note muted">
              {tr("wsbar.usage.plan", { plan })}
              {usage?.canUpgrade ? " · " + tr("wsbar.usage.copilot.upgradable") : ""}
            </div>
          )}
          {unavailable ? (
            <div className="wu-note muted">{tr("wsbar.usage.unavailable_note", { name: "GitHub Copilot" })}</div>
          ) : (
            wins.map((w) => (
              <UsageRow
                key={w.label}
                label={w.label}
                w={{ pct: w.pct, until: w.resetsAt ? untilText(w.resetsAt) : "", when: w.resetsAt ? whenText(w.resetsAt) : "" }}
              />
            ))
          )}
          <div className="wu-foot">
            {!unavailable && <span className="wu-ago muted">{tr("wsbar.usage.fetched", { ago: agoText(usage?.ageSec) })}</span>}
            <button type="button" className="ghost wu-reload" onClick={refresh} disabled={refreshing}>
              <Icon name="refresh" spin={refreshing} /> {tr("wsbar.usage.refresh")}
            </button>
          </div>
          <UsageBreakdownLink onNavigate={() => setOpen(false)} />
          {/* Manage link on its own row (same as the generic UsageChip) — cramming it
              into wu-foot alongside 取得/更新 broke the layout. */}
          <a className="wu-manage" href="https://github.com/settings/copilot" target="_blank" rel="noopener">
            <Icon name="link-external" /> {tr("wsbar.usage.open_page")}
          </a>
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
      return t("wsbar.state.running");
    case "starting":
      return t("wsbar.state.starting");
    case "none":
    case "stopped":
      return t("wsbar.state.stopped");
    case "starting…":
      return t("wsbar.state.starting");
    case "stopping…":
      return t("wsbar.state.stopping");
    case "recreating…":
      return t("wsbar.state.recreating");
    case "unknown":
      return t("wsbar.state.unknown");
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
    // key は静的な k のみ — title は live 値（mem 使用量など）を含むので混ぜると
    // 値が変わるたびに remount されてスパークラインがちらつく。
    <span className={"ws-graph" + (level === 2 ? " crit" : level === 1 ? " warn" : "")} title={title} key={k}>
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
  const restartWs = useWorkspaceStore((s) => s.restart);
  const wsStale = useWorkspaceStore((s) => s.stale);
  const tenant = useTenantStore((s) => s.tenant);
  const superAdmin = useTenantStore((s) => s.superAdmin);
  const layout = useLayoutStore((s) => s.layout);
  const splitRight = useLayoutStore((s) => s.splitRight);
  const splitDown = useLayoutStore((s) => s.splitDown);
  const resetToTerminal = useLayoutStore((s) => s.resetToTerminal);
  const openPaneTarget = useLayoutStore((s) => s.openTarget);
  const activePaneId = layout.activeId;
  const openStart = useSessionsStore((s) => s.openStart);
  const askConfirm = useConfirm();
  const tr = useT();
  const { wsStats, wsHist, hostStats, hostHist } = useWsResourceChips(tenant, superAdmin);
  const isMobile = useIsMobile();
  const [port, setPort] = useState("");
  const [previewPath, setPreviewPath] = useState("/");
  const [pvOpen, setPvOpen] = useState(false); // desktop port-preview popover
  // Live Chromium attachments, read when the popover opens (no polling: this is
  // a recovery entry, and the authoritative state is the pane's own socket).
  const [attachments, setAttachments] = useState<BrowserAttachmentStatus[]>([]);
  const [staleOpen, setStaleOpen] = useState(false); // 要再起動 badge popover
  const [moreOpen, setMoreOpen] = useState(false); // mobile overflow popover
  const [resOpen, setResOpen] = useState(false); // desktop resource-tiles popover
  // Keyboard: Ctrl/⌘+K g r toggles the resource-tiles popover (desktop).
  useOpenSignal("resources", () => setResOpen((o) => !o));
  const pvRef = useRef<HTMLDivElement>(null);
  const staleRef = useRef<HTMLDivElement>(null);
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
  const canSplitRight = !isMobile && layout.cols.length < (layout.mode === "tabs" ? MAX_TAB_COLS : 4);
  const canSplitDown = isMobile ? totalPanes < 2 : (activeCol ? activeCol.panes.length : totalPanes) < 2;

  // はじめる while stopped: don't dead-end (起動導線 Ph3) — confirm, start the
  // workspace, and open the hub once the 4s poll reports running. startQueued
  // survives the whole starting window; a second click while queued re-confirms
  // harmlessly (startWs is only re-fired from a genuinely stopped state).
  const [startQueued, setStartQueued] = useState(false);
  useEffect(() => {
    if (!startQueued) return;
    if (running) {
      setStartQueued(false);
      openStart();
    } else if (!busy) {
      // 起動失敗・外部停止などで stopped/none/unknown に落ち着いた: running には
      // 二度と遷移しないので、ここで解除しないと はじめる がスピナーのまま固着する。
      // （クリック直後は start() が同バッチで "starting…" を積むため誤解除しない。）
      setStartQueued(false);
    }
  }, [startQueued, running, busy, openStart]);
  const onStart = async () => {
    if (running) {
      openStart();
      return;
    }
    const ok = await askConfirm({
      title: tr("wsbar.confirm.start.title"),
      body: tr("wsbar.confirm.start.body"),
      confirmLabel: busy ? tr("wsbar.confirm.start.wait") : tr("wsbar.confirm.start.go"),
      danger: false,
    });
    if (!ok) return;
    setStartQueued(true);
    if (!busy) void startWs();
  };

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
      title: tr("wsbar.stop_ws"),
      body: tr("wsbar.confirm.stop.body"),
      confirmLabel: tr("wsbar.confirm.stop.go"),
      danger: false,
    });
    if (ok) void stopWs();
  };

  // Backend drift (CP-detected): the running container predates the deployed build,
  // so a stop→start would pick up the new backend. Shown only while genuinely
  // running — never mid-transition, where the restart is already happening. It is a
  // state, not an event: it clears itself once the workspace is back on the current
  // build, so there is no dismiss.
  const staleShown = wsStale && running && !busy;
  useEffect(() => {
    if (!staleShown) setStaleOpen(false);
  }, [staleShown]);

  // Apply the backend update: stop→start, keeping repos and everything on disk
  // (NOT recreate). Sessions stop and are resumable, so name the count up front —
  // that's the whole cost of the action.
  const onRestart = async () => {
    setStaleOpen(false);
    const live = useSessionsStore.getState().sessions.filter((s) => s.alive).length;
    const ok = await askConfirm({
      title: tr("wsbar.confirm.restart.title"),
      body: live > 0 ? tr("wsbar.confirm.restart.body_live", { n: live }) : tr("wsbar.confirm.restart.body"),
      confirmLabel: tr("wsbar.confirm.restart.go"),
      danger: false,
    });
    if (ok) void restartWs();
  };

  // Open a service the user started inside the container (e.g. a Spring Boot app
  // on :8080) in a new tab, proxied through the CP /preview/{port}.
  const openPreview = () => {
    const p = port.trim();
    if (!running || !browserTarget(Number(p), "/")) return;
    window.open(previewURL(p, previewPath.trim()), "_blank", "noopener");
    setPvOpen(false);
    setMoreOpen(false);
  };

  const openBrowserPane = () => {
    if (!running) return;
    const target = browserTarget(Number(port.trim()), previewPath.trim());
    if (!target) return;
    openPaneTarget({ content: { kind: "browser", ...target } });
    setPvOpen(false);
    setMoreOpen(false);
  };

  // Read the live Chromium attachments when a popover that shows them opens.
  // Deliberately not polled: the pane's own socket is the authoritative state,
  // and this list only has to be right at the moment the user looks at it.
  useEffect(() => {
    if (!pvOpen && !moreOpen) return;
    if (!running) {
      setAttachments((prev) => (prev.length ? [] : prev));
      return;
    }
    let cancelled = false;
    void listBrowserAttachments().then(
      (list) => !cancelled && setAttachments(list),
      () => !cancelled && setAttachments((prev) => (prev.length ? [] : prev)),
    );
    return () => {
      cancelled = true;
    };
  }, [pvOpen, moreOpen, running]);

  // Close each popover on an outside click / Escape.
  useDismiss(pvRef, pvOpen, () => setPvOpen(false));
  useDismiss(staleRef, staleOpen, () => setStaleOpen(false));
  useDismiss(moreRef, moreOpen, () => setMoreOpen(false));
  useDismiss(resRef, resOpen, () => setResOpen(false));

  // --- pieces shared between the inline (desktop) and folded (mobile) layouts ---
  const lvl = (v: number | null, warn: number, crit: number) => (v == null ? 0 : v >= crit ? 2 : v >= warn ? 1 : 0);

  // Container (own workspace): memory fill (vs quota) + CPU%. Shown to everyone.
  const hasWs = wsStats && wsStats.running && wsStats.mem_used != null;
  // hasWs が mem_used != null を保証するが、TS のプロパティ narrowing は alias 越しに
  // 届かないので ?? 0 で明示（到達しないフォールバック）。
  const memRatio = hasWs && wsStats.mem_max ? (wsStats.mem_used ?? 0) / wsStats.mem_max : null;
  // A process in this container was OOM-killed within the last few minutes (the
  // container itself survived). Flag the memory tile crit so it's noticed even after
  // usage falls back — a build/agent likely just died. (metrics.go oom_recent.)
  const oomRecent = !!(hasWs && wsStats.oom_recent);
  const containerTiles = hasWs && (
    <>
      {tile({
        k: "mem",
        series: wsHist.map((p) => p.mem),
        max: 1,
        track: true,
        value: memRatio != null ? `${Math.round(memRatio * 100)}%` : `${fg(wsStats.mem_used ?? 0)}G`,
        level: oomRecent ? 2 : lvl(memRatio, 0.75, 0.9),
        title: oomRecent
          ? tr("wsbar.tile.ws_mem", { mem: `${fg(wsStats.mem_used ?? 0)}${wsStats.mem_max ? "/" + fg(wsStats.mem_max) : ""}` }) +
            "\n" +
            tr("wsbar.tile.ws_mem_oom_note")
          : tr("wsbar.tile.ws_mem", { mem: `${fg(wsStats.mem_used ?? 0)}${wsStats.mem_max ? "/" + fg(wsStats.mem_max) : ""}` }),
      })}
      {tile({
        k: "cpu",
        series: wsHist.map((p) => p.cpu),
        value: wsStats.cpu_pct != null ? `${Math.round(wsStats.cpu_pct)}%` : "–",
        level: lvl(wsStats.cpu_pct ?? null, 60, 90),
        title: tr("wsbar.tile.ws_cpu"),
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
        title: tr("wsbar.tile.host_load", { load: Number(hostStats.load1).toFixed(2), ncpu: hostStats.ncpu ?? "?" }),
      })}
      {tile({
        k: "mem",
        series: hostHist.map((p) => p.mem),
        max: 1,
        track: true,
        value: hostMemRatio != null ? `${Math.round(hostMemRatio * 100)}%` : "",
        level: lvl(hostMemRatio, 0.8, 0.92),
        title: tr("wsbar.tile.host_mem", { mem: `${fg(hostStats.mem_used)}/${fg(hostStats.mem_total)}` }),
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
        title={tr("wsbar.resources_title")}
        aria-expanded={resOpen}
        onClick={() => setResOpen((o) => !o)}
      >
        <Icon name="pulse" />
        <span className="ws-res-sum">{resSummary || tr("wsbar.resources")}</span>
        <Icon name="chevron-down" />
      </button>
      {resOpen && <div className="ws-res-pop">{graphs}</div>}
    </div>
  );

  // Subscription-usage chips, one per agent that exposes limits (Claude, Codex,
  // Antigravity — display order matches the agent order). Each self-hides until its
  // endpoint answers, so a claude-only user sees one chip, a user of all sees three.
  // The 8px bar gap spaces them (no manual dividers needed).
  const usageChips = (
    <>
      {USAGE_SOURCES.map((s) => (
        <UsageChip key={s.endpoint} src={s} tenant={tenant} />
      ))}
      <CopilotUsageChip tenant={tenant} />
      <AgyUsageChip tenant={tenant} />
    </>
  );

  const statsBlock = (
    <>
      {graphs}
      {usageChips}
    </>
  );

  const previewPop = (
    <>
      <div className="pv-section">
        <label className="pv-label">{tr("wsbar.preview.port_label")}</label>
        <div className="pv-row">
          <input
            className="preview-port"
            type="number"
            min="1"
            max="65535"
            placeholder={tr("wsbar.preview.port_ph")}
            value={port}
            onChange={(e) => setPort(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && openBrowserPane()}
            title={tr("wsbar.preview.port_hint")}
          />
          <input
            className="preview-path"
            value={previewPath}
            onChange={(e) => setPreviewPath(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && openBrowserPane()}
            aria-label={tr("browser.path")}
            title={tr("wsbar.preview.path_hint")}
          />
        </div>
        <div className="pv-row pv-actions">
          <button onClick={openBrowserPane} disabled={!running || !browserTarget(Number(port.trim()), previewPath.trim())}>
            {tr("wsbar.preview.open_pane")}
          </button>
          {/* openPreview と同じ browserTarget 検証で無効化 — 範囲外/7700 ポートで
              「押せるのに無反応」にならないように（ペインで開く ボタンと同型）。 */}
          <button onClick={openPreview} disabled={!running || !browserTarget(Number(port.trim()), "/")}>
            {tr("wsbar.preview.open_light")}
          </button>
        </div>
        <div className="pv-hint">{tr("wsbar.preview.hint")}</div>
      </div>
      {/* エージェントが attach した既存 Chromium への戻り道（docs/53 §53.7）。
          本来の入口はミラーの action リンクだが、会話が流れてリンクを見失ったり
          ペインを閉じた後でも、生きている接続へ戻れるようにする。 */}
      {attachments.length > 0 && (
        <div className="pv-section">
          <label className="pv-label">{tr("wsbar.preview.attachments_label")}</label>
          {attachments.map((a) => (
            <div className="pv-row pv-actions" key={a.id}>
              <button
                className="pv-attachment"
                onClick={() => {
                  setPvOpen(false);
                  setMoreOpen(false);
                  void openBrowserAttachment(a.id);
                }}
                title={a.url || a.title}
              >
                {a.title || tr("browser.attach.canvas")}
              </button>
            </div>
          ))}
        </div>
      )}
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
        className={"ws-power " + (running ? "on" : "off") + (staleShown ? " stale" : "")}
        onClick={onToggle}
        disabled={busy}
        title={
          (running ? tr("wsbar.stop_ws") : tr("wsbar.start_ws")) + (staleShown ? " — " + tr("wsbar.stale.title") : "")
        }
        aria-label={running ? tr("wsbar.stop_ws") : tr("wsbar.start_ws")}
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
        className={"ws-state" + (wsState === "stopped" && wsStats?.oom_killed ? " warn" : "")}
        title={
          wsState === "none"
            ? tr("wsbar.state_title.none")
            : wsState === "stopped"
              ? wsStats?.oom_killed
                ? tr("wsbar.state_title.oom", { code: wsStats.exit_code ?? "?" })
                : wsStats?.exit_code
                  ? tr("wsbar.state_title.exited", { code: wsStats.exit_code })
                  : tr("wsbar.state_title.stopped")
              : wsState === "starting"
                ? tr("wsbar.state_title.starting")
                : tr("wsbar.state_title.other", { state: wsState })
        }
      >
        {wsLabel(wsState)}
      </span>
      {/* 要再起動 — the backend moved on while this container kept running. Sits
          right of the state chip (and dots the power button next to it) because the
          fix IS the power toggle; the popover explains the cost and offers the
          stop→start in one click. Label folds away on a phone, tap target stays. */}
      {staleShown && (
        <div className="ws-stale" ref={staleRef}>
          <button
            className="ws-stale-pill"
            onClick={() => setStaleOpen((o) => !o)}
            aria-expanded={staleOpen}
            title={tr("wsbar.stale.title")}
          >
            <Icon name="refresh" />
            <span className="lbl">{tr("wsbar.stale.badge")}</span>
          </button>
          {staleOpen && (
            <div className="ws-stale-pop">
              <div className="ws-stale-txt">{tr("wsbar.stale.body")}</div>
              <div className="ws-stale-actions">
                <button className="ws-stale-go" onClick={() => void onRestart()}>
                  {tr("wsbar.stale.restart")}
                </button>
                <button className="ghost" onClick={() => setStaleOpen(false)}>
                  {tr("wsbar.stale.later")}
                </button>
              </div>
            </div>
          )}
        </div>
      )}
      {/* はじめる — the single "start anything" entry (起動導線 Ph2): opens the
          StartModal hub (chat / repo / clone / home / その他). While the workspace
          is stopped it offers to start it and opens the hub when ready (Ph3). */}
      <button
        className="ghost ws-split ws-newsession"
        title={
          running
            ? tr("wsbar.start_here.running") + hintSuffix("session.new")
            : startQueued
              ? tr("wsbar.start_here.queued")
              : tr("wsbar.start_here.stopped")
        }
        onClick={() => void onStart()}
      >
        <Icon name={startQueued ? "loading" : "add"} spin={startQueued} />
        <span className="lbl">{tr("wsbar.start_here")}</span>
      </button>
      <button
        className="ghost ws-split"
        title={tr("wsbar.split_right") + hintSuffix("pane.splitRight")}
        disabled={!canSplitRight}
        onClick={() => splitRight()}
      >
        <Icon name="split-horizontal" />
        <span className="lbl">{tr("wsbar.split_right")}</span>
      </button>
      <button
        className="ghost ws-split"
        title={tr("wsbar.split_down_title") + hintSuffix("pane.splitDown")}
        disabled={!canSplitDown}
        onClick={() => activePaneId && splitDown(activePaneId)}
      >
        <Icon name="split-vertical" />
        <span className="lbl">{tr("wsbar.split_down")}</span>
      </button>
      <button
        className="ghost ws-closeall"
        title={tr("wsbar.close_all_title") + hintSuffix("pane.closeAll")}
        disabled={!canCloseAll}
        onClick={() => resetToTerminal()}
      >
        <Icon name="close-all" />
        <span className="lbl">{tr("wsbar.close_all")}</span>
      </button>

      <span className="ws-spacer" />

      {isMobile ? (
        <div className="ws-more" ref={moreRef}>
          <button
            className="ghost ws-more-btn"
            title={tr("wsbar.more_title")}
            onClick={() => setMoreOpen((o) => !o)}
          >
            <Icon name="ellipsis" />
          </button>
          {moreOpen && (
            <div className="ws-more-pop">
              {/* statsBlock は常に truthy な Fragment（チップは各自 null で自己非表示）なので
                  ここでは判定できない — 空のときの余白は CSS の :empty で畳む（wsbar.css）。 */}
              <div className="ws-more-stats">{statsBlock}</div>
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
              title={tr("wsbar.preview_title")}
              aria-expanded={pvOpen}
              onClick={() => setPvOpen((o) => !o)}
            >
              <Icon name="globe" /> {tr("wsbar.preview")} <Icon name="chevron-down" />
            </button>
            {pvOpen && <div className="ws-preview-pop">{previewPop}</div>}
          </div>
        </>
      )}
    </div>
  );
}
