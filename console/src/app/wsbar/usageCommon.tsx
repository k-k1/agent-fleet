import { useCallback, useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { fmtDateTime, TIME_HM } from "../../lib/intl.ts";
import { t, tCount, useT } from "../../lib/i18n/index.ts";
import { useSettingsUI } from "../../features/settings/store.ts";

// The reading shared by the usage chips (claude/codex/agy/copilot). Each agent uses
// different fields (fiveHour/sevenDay vs groups vs quotas), expressed as one type with
// optionals rather than a union, because the chips already branch on field presence.
export interface UsageWindow {
  pct: number;
  resetsAt: string;
  // The window rolled over after the capture, so the % is an estimate assuming zero use
  // since. It is not an observation, so chip and row show "—" rather than asserting 0%,
  // which would be indistinguishable from a dead capture. claude and codex return this.
  stale?: boolean;
  // derived: codex's flag for the same rolled window (ADR 0111 decision 6).
  derived?: boolean;
}
export interface UsageReading {
  ok?: boolean;
  authed?: boolean;
  ageSec?: number;
  // The provider's own observation time and the opaque login epoch, for the limit board's
  // browser fallback (usageResetNotify.ts). claude and codex only.
  observedAt?: string;
  limitEpoch?: string;
  limitEpochSince?: string;
  user?: string;
  plan?: string;
  planType?: string;
  account?: string;
  fiveHour?: UsageWindow;
  sevenDay?: UsageWindow;
  resetCredits?: { availableCount?: number; credits?: { expiresAt?: string }[] };
  // agy: a pool per model group (weekly, plus a 5-hour window on paid tiers), remaining-based.
  groups?: {
    label: string;
    remainingPct: number;
    resetsAt?: string;
    // stale: the reset passed since the scrape, so the remaining % is unknown (see UsageWindow.stale).
    stale?: boolean;
    fiveHour?: { remainingPct: number; resetsAt?: string; stale?: boolean };
  }[];
  // copilot: a quota per pool plus one shared monthly reset.
  quotas?: { id: string; remainingPct: number }[];
  resetsAt?: string;
  canUpgrade?: boolean;
}

// useUsage surfaces one agent's subscription usage (5-hour + weekly) from `endpoint`
// (api/claude/usage or api/codex/usage). The Console reads the Agent's value once on
// mount, on a slow 5-min re-read (to recover the chip if the workspace started after
// mount / stay in sync across tabs), and on an explicit refresh() (?refresh=1) — never
// on a fast background timer (the claude endpoint is unofficial/rate-limited; codex is
// a local rollout read). Kept in WsBar so it doesn't re-render the app.
export function useUsage(tenant: string | null, endpoint: string) {
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

// agoText: seconds since the usage was fetched -> "N seconds/minutes/hours ago" (freshness label).
export function agoText(sec: number | null | undefined) {
  if (sec == null) return "";
  if (sec >= 3600) return tCount("wsbar.ago_hour", Math.round(sec / 3600));
  if (sec >= 60) return tCount("wsbar.ago_min", Math.round(sec / 60));
  return tCount("wsbar.ago_sec", Math.max(0, sec));
}

// UsageBreakdownLink: deep link from the usage chip into the Settings > agent usage tab
// (docs/log/46 §5). The chip answers "how much of the subscription window is left"; "what
// were the tokens spent on" is a separate question, and this hands the user straight over
// at the moment they ask it.
export function UsageBreakdownLink({ onNavigate }: { onNavigate: () => void }) {
  const tr = useT();
  const openSettings = useSettingsUI((s) => s.openSettings);
  return (
    <Button
      className="wu-manage"
      onClick={() => {
        onNavigate();
        openSettings("usage");
      }}
    >
      <Icon name="graph" /> {tr("wsbar.usage.breakdown")}
    </Button>
  );
}

// UsageRow: one limit window in the usage dropdown — label + percent, a fill bar, and
// the reset shown both relatively ("in N hours") and as an absolute date-time. A stale
// window (the reading predates the window it describes, so its 0% is an assumption the
// agent could not verify) shows "—" and an empty bar instead: a confident 0% next to a
// reset time is indistinguishable from a real one, which is exactly how a dead capture
// went unnoticed for hours.
export function UsageRow({ label, w }: { label: string; w: { pct: number; until: string; when: string; stale?: boolean } }) {
  const level = w.stale ? "" : w.pct >= 95 ? " crit" : w.pct >= 80 ? " warn" : "";
  return (
    <div className="wu-row">
      <div className="wu-row-head">
        <span className="wu-label">{label}</span>
        <span className={"wu-pct" + (w.stale ? " muted" : level)} title={w.stale ? t("wsbar.usage.stale_note") : undefined}>
          {w.stale ? "—" : `${w.pct}%`}
        </span>
      </div>
      <div className="wu-bar">
        <span className={"wu-bar-fill" + level} style={{ width: (w.stale ? 0 : Math.min(100, w.pct)) + "%" }} />
      </div>
      <div className="wu-reset muted">{t("wsbar.usage.reset_at", { until: w.until, when: w.when })}</div>
      {w.stale && <div className="wu-note muted">{t("wsbar.usage.stale_note")}</div>}
    </div>
  );
}

// untilText: a reset instant -> a compact relative "in N days/hours/minutes" (the weekday+time
// the app shows is hard to read; relative is glanceable, the absolute date-time goes
// in the tooltip). whenText: the absolute local "M/D HH:MM".
export function untilText(iso: string) {
  const ms = new Date(iso).getTime();
  if (isNaN(ms)) return "";
  const min = Math.max(0, Math.round((ms - Date.now()) / 60000));
  if (min >= 1440) return tCount("common.days_left", Math.round(min / 1440));
  if (min >= 60) return tCount("wsbar.until_hour", Math.round(min / 60));
  return tCount("wsbar.until_min", min);
}
export const whenText = (iso: string) => fmtDateTime(iso);

export function expiryText(iso: string) {
  const ms = new Date(iso).getTime();
  if (isNaN(ms)) return "";
  const days = Math.ceil((ms - Date.now()) / 86400000);
  if (days <= 0) return t("wsbar.expiry_today");
  return days === 1 ? t("wsbar.expiry_tomorrow") : tCount("common.days_left", days);
}

// resetChipText: reset instant → the compact form shown ON the chip when a window is
// near its cap. Same-day resets drop the date (just HH:MM); a later day keeps M/D so the
// day is unambiguous.
export function resetChipText(iso: string) {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const now = new Date();
  const sameDay =
    d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
  return sameDay ? fmtDateTime(d, TIME_HM) : fmtDateTime(d);
}
