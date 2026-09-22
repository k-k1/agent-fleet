// Which agent-usage chips keep a permanent slot on the WS bar, and which fold away.
//
// The bar grew one subscription-limit chip per agent that exposes one (claude, codex,
// muse, copilot, agy). Five of them take the width the pane buttons need, and most of
// them read "0% / 2%" — an agent nobody here runs. So the bar shows the agents actually
// in use and folds the rest behind a single "+N" chip.
//
// "In use" is read from the session list the Console already polls: a LIVE session of
// kind K means K is in use right now; otherwise the newest session of that kind dates
// it. The derived stamps are mirrored to localStorage because the list alone is not a
// memory — it renders before the first poll answers, and sessions get archived. An agent
// run daily for a month must not fall behind one tried once yesterday just because its
// sessions were cleaned up.
//
// The user's own word overrides the ranking in both directions (pin / always fold); see
// planUsageChips for the exact precedence.

const KEY = "af.kind-lastused";

// How many auto-ranked chips stay on the bar. Deliberately not a setting: "I want a third
// one" is already expressible — pin it — and a count control would have to live somewhere
// that stays reachable when nothing is folded.
export const AUTO_INLINE = 2;

/** Per session kind: when it was last in use, epoch ms. */
export type KindStamps = Record<string, number>;

/** Only the session fields the ranking reads (the store's rows carry much more). */
export interface KindUseSession {
  kind?: string;
  alive?: boolean;
  createdAt?: string;
}

function readRaw(): KindStamps {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(KEY) || "{}");
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
    const out: KindStamps = {};
    for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
      if (typeof v === "number" && isFinite(v)) out[k] = v;
    }
    return out;
  } catch {
    return {};
  }
}

export function readKindStamps(): KindStamps {
  return readRaw();
}

// A live session re-stamps "now" on every poll, so the value is quantised to the minute:
// without it the 4s list poll would rewrite localStorage forever for no visible gain.
const LIVE_GRAIN_MS = 60000;

/**
 * noteSessionKinds folds a session list into the stored stamps and returns the merged map.
 * Monotonic — a stamp only ever moves forward, which is what makes an archived history
 * survive its sessions.
 */
export function noteSessionKinds(list: readonly KindUseSession[], now = Date.now()): KindStamps {
  const cur = readRaw();
  const next: KindStamps = { ...cur };
  const live = Math.floor(now / LIVE_GRAIN_MS) * LIVE_GRAIN_MS;
  for (const s of list || []) {
    const kind = s?.kind;
    if (!kind) continue;
    const at = s.alive ? live : Date.parse(s.createdAt || "");
    if (!isFinite(at)) continue;
    // NaN-safe: an absent current value fails the comparison and gets written.
    if (!(next[kind] >= at)) next[kind] = at;
  }
  let changed = Object.keys(next).length !== Object.keys(cur).length;
  if (!changed) for (const k of Object.keys(next)) changed ||= next[k] !== cur[k];
  if (changed) {
    try {
      localStorage.setItem(KEY, JSON.stringify(next));
    } catch {
      /* private mode / quota — the in-memory ranking still works for this page */
    }
  }
  return next;
}

export interface ChipPlanInput {
  /** Every chip that could exist, in the order the bar draws them. */
  order: readonly string[];
  /** Chips that have something to show right now (the others render nothing at all). */
  visible: readonly string[];
  /** Chips at or over the cap, or holding reset credits — news the bar must not swallow. */
  urgent?: readonly string[];
  /** Explicitly kept on the bar by the user. */
  pinned?: readonly string[];
  /** Explicitly folded by the user. */
  folded?: readonly string[];
  stamps?: KindStamps;
  inline?: number;
}

export interface ChipPlan {
  /** Chips on the bar, in declaration order. */
  bar: string[];
  /** Chips inside the "+N" popover, most recently used first. */
  fold: string[];
}

/**
 * planUsageChips splits the visible chips into the bar and the fold popover.
 *
 * Precedence, highest first:
 *   1. "always fold" — the user said they do not run this agent. It beats the urgency
 *      promotion below: a near-cap warning about an agent you never launch is not news,
 *      and the chip is still one click away rather than gone.
 *   2. "pin" — always on the bar, however long ago it was last used. Pins spend the same
 *      `inline` budget as the ranking, so pinning two agents means exactly those two: the
 *      bar only grows past the budget when the user pins more chips than it holds.
 *   3. the `inline` most recently used of what is left.
 *   4. urgent (>= 95% / reset credits) — added ON TOP of the budget if the ranking did not
 *      already place it. Such a chip has stopped showing a percentage and started showing
 *      when you are unblocked, and evicting the agent you are actually working in to say so
 *      would trade the news for the thing you look at.
 *
 * The bar is emitted in declaration order, never in rank order: which chips are on the bar
 * may change with use, but the ones that stay must not swap places under the pointer.
 */
export function planUsageChips(input: ChipPlanInput): ChipPlan {
  const { order, visible } = input;
  const inline = input.inline ?? AUTO_INLINE;
  const stamps = input.stamps || {};
  const urgent = new Set(input.urgent || []);
  const pinned = new Set(input.pinned || []);
  const folded = new Set(input.folded || []);
  const idx = new Map(order.map((k, i) => [k, i]));
  const shown = order.filter((k) => visible.includes(k));
  // Most recent first; never used sorts last, ties keep declaration order.
  const byUse = (a: string, b: string) =>
    (stamps[b] || 0) - (stamps[a] || 0) || (idx.get(a) ?? 0) - (idx.get(b) ?? 0);

  const bar = new Set<string>();
  const rest: string[] = [];
  for (const k of shown) {
    if (folded.has(k)) continue;
    if (pinned.has(k)) bar.add(k);
    else rest.push(k);
  }
  rest.sort(byUse);
  const auto = rest.slice(0, Math.max(0, inline - bar.size));
  for (const k of auto) bar.add(k);
  for (const k of rest) if (urgent.has(k)) bar.add(k);
  return {
    bar: shown.filter((k) => bar.has(k)),
    fold: shown.filter((k) => !bar.has(k)).sort(byUse),
  };
}
