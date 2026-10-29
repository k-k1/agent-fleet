import { type OpenTarget } from "../../core/store/uiOpen.ts";
import type { SessionKind } from "../../types/session.ts";
import type { MsgKey, t } from "../../lib/i18n/index.ts";
import { usedFromRemaining } from "../../features/sessions/move.ts";
import type { UsageReading } from "./usageCommon.tsx";

// A usage source = one agent's subscription-limit chip (Claude / Codex). Both endpoints
// return the same {ok, fiveHour, sevenDay, ageSec} shape (codex reads its rate_limits
// straight from the rollout — no network), so one chip component renders either.
export interface UsageSource {
  endpoint: string;
  key: OpenTarget; // keyboard-open target id (uiOpen signal)
  // The agent this chip reports for. Its glyph, color class and short name are READ FROM THE
  // REGISTRY (lib/sessionkind), never restated here: this table used to carry its own copies
  // and they went stale the moment the kinds moved to brand marks, leaving the WS bar as the
  // last place still drawing the old codicons.
  kind: SessionKind;
  fiveLabelKey: MsgKey; // 5-hour window label (i18n key)
  weekLabelKey: MsgKey; // weekly window label (i18n key)
  // live = the endpoint queries the current usage (claude), so a refresh button makes
  // sense. When false (codex), the reading is a snapshot from the last turn — no
  // manual refresh; a note explains it instead.
  live: boolean;
  // noteKey = a caveat about what the numbers ARE, shown under the rows. Not tied to
  // `live`: claude's reading is live and still needs one, because the source rounds.
  noteKey?: MsgKey;
  // manageURL = the agent vendor's own usage/limits page (opened in a new tab from the
  // dropdown), so the user can jump to the authoritative source for the exact numbers.
  manageURL?: string;
}

// Exported for WsBarUsageFold.dom.test.tsx: the folding is a property of the chips as a
// GROUP, so the test mounts the real chips inside the real group rather than a stand-in.
export const USAGE_SOURCES: UsageSource[] = [
  {
    endpoint: "api/claude/usage",
    key: "usage-claude",
    kind: "claude",
    fiveLabelKey: "wsbar.usage.claude.five",
    weekLabelKey: "wsbar.usage.claude.week",
    live: true,
    noteKey: "wsbar.usage.claude.note",
    manageURL: "https://claude.ai/new#settings/usage",
  },
  {
    endpoint: "api/codex/usage",
    key: "usage-codex",
    kind: "codex",
    fiveLabelKey: "wsbar.usage.codex.five",
    weekLabelKey: "wsbar.usage.codex.week",
    live: false,
    noteKey: "wsbar.usage.codex.note",
    manageURL: "https://chatgpt.com/#settings/Usage",
  },
  {
    // muse (ADR 0095 decision 10). Its two windows fill the same two rows, but neither the
    // reading nor the first row's NAME is what the others' are: the value is the last
    // observation a running host pushed over the wire, and the window's length is a wire field
    // (measured 300 minutes) rather than a constant — so the row says "current window" and the
    // note says what was measured, instead of the label asserting five hours.
    //
    // live is false for the reason codex's is: there is nothing to refresh TOWARDS. Asking
    // again returns the same observation until a muse turn completes.
    endpoint: "api/muse/usage",
    key: "usage-muse",
    kind: "muse",
    fiveLabelKey: "wsbar.usage.muse.five",
    weekLabelKey: "wsbar.usage.muse.week",
    live: false,
    noteKey: "wsbar.usage.muse.note",
    manageURL: "https://dev.meta.ai/usage",
  },
];

// A remaining-pool source = an agent whose endpoint reports what is LEFT of each quota pool
// (Copilot, Antigravity) rather than the two used-percent windows above. One chip
// (RemainingUsageChip) renders either; everything that differs between them is a field here,
// so the chip never branches on which agent it is.
export interface RemainingPool {
  label: string;
  pct: number; // USED percent, converted from the endpoint's remaining percent
  resetsAt?: string;
  stale?: boolean;
}
export interface RemainingSource {
  endpoint: string;
  key: OpenTarget;
  kind: SessionKind;
  name: string; // the product name the titles and notes use (not the chip's short label)
  // pools: the dropdown rows, in order, from an ok reading. Empty means "unavailable".
  pools(u: UsageReading, tr: typeof t): RemainingPool[];
  // chipNums: what the chip reads while no pool is near its cap (pools is non-empty).
  chipNums(u: UsageReading, pools: RemainingPool[]): string;
  titleKey?: MsgKey; // the idle chip title; default wsbar.usage.title with {name}
  accountField: "account" | "user"; // which reading field names the signed-in account
  upgradableKey?: MsgKey; // appended to the plan line when the reading says canUpgrade
  noteKey?: MsgKey; // a caveat shown under the rows
  manageURL?: string;
}

const used = usedFromRemaining;

const COPILOT_POOL_KEYS: Record<string, MsgKey> = {
  chat: "wsbar.usage.copilot.pool_chat",
  completions: "wsbar.usage.copilot.pool_completions",
  premium_interactions: "wsbar.usage.copilot.pool_premium",
};

// GitHub Copilot's account credit. The backend reads copilot_internal/user (gh transparent
// auth) and returns {plan, sku, resetsAt, quotas:[{id, remainingPct}]}; every quota pool
// (chat / completions / premium_interactions, plan-dependent) shares the one monthly reset.
export const COPILOT_USAGE: RemainingSource = {
  endpoint: "api/copilot/usage",
  key: "usage-copilot",
  kind: "copilot",
  name: "GitHub Copilot",
  pools: (u, tr) =>
    (Array.isArray(u.quotas) ? u.quotas : []).map((q) => ({
      // Unknown pool ids fall back to the raw id.
      label: Object.hasOwn(COPILOT_POOL_KEYS, q.id) ? tr(COPILOT_POOL_KEYS[q.id]) : q.id,
      pct: used(q.remainingPct),
      resetsAt: u.resetsAt,
    })),
  // The primary pool: the backend orders quotas premium first on paid plans, else chat.
  chipNums: (_u, pools) => `${pools[0].pct}%`,
  titleKey: "wsbar.usage.copilot.title",
  accountField: "user",
  upgradableKey: "wsbar.usage.copilot.upgradable",
  manageURL: "https://github.com/settings/copilot",
};

// Antigravity's quota (docs/log/32). agy's wallet is split into per-model-group pools
// (Gemini / Claude+GPT), each with a weekly window plus a 5-hour window on paid tiers, and the
// agent reports REMAINING percent (matching the TUI's own /usage bars).
export const AGY_USAGE: RemainingSource = {
  endpoint: "api/connections/agy/usage",
  key: "usage-agy",
  kind: "agy",
  name: "Antigravity",
  // Per group the weekly window, then the 5h one.
  pools: (u, tr) =>
    (Array.isArray(u.groups) ? u.groups : []).flatMap((g) => [
      { label: tr("wsbar.usage.agy.week_row", { group: g.label }), pct: used(g.remainingPct), resetsAt: g.resetsAt, stale: !!g.stale },
      ...(g.fiveHour
        ? [
            {
              label: tr("wsbar.usage.agy.five_row", { group: g.label }),
              pct: used(g.fiveHour.remainingPct),
              resetsAt: g.fiveHour.resetsAt,
              stale: !!g.fiveHour.stale,
            },
          ]
        : []),
    ]),
  // The first group (Gemini, agy's default-model pool), 5h% / weekly% — the other chips' order.
  chipNums: (u) => {
    const g0 = u.groups![0];
    return [g0.fiveHour && (g0.fiveHour.stale ? "—" : `${used(g0.fiveHour.remainingPct)}%`), g0.stale ? "—" : `${used(g0.remainingPct)}%`]
      .filter(Boolean)
      .join(" / ");
  },
  accountField: "account",
  // The Starter pool is tiny and shared with the Antigravity IDE / Jules (docs/log/32 Track C-3).
  noteKey: "agents.agy_exp_label",
};

// In bar order, after USAGE_SOURCES.
export const REMAINING_SOURCES: RemainingSource[] = [COPILOT_USAGE, AGY_USAGE];
