// Types + pure lookup logic for the keyboard system. No store / DOM imports — this
// stays lib-pure and testable; the command DATA lives in features/keys/commands.ts and
// is passed into these lookups. The registry is the single source of truth the
// dispatcher, which-key overlay and command palette all read, so display and behavior
// never drift.
import { canonical } from "./chords.ts";

export type Region = "rail" | "main" | "bars";

// What the dispatcher knows when it decides whether a command applies. Built fresh per
// keydown from the DOM (activeElement), the layout store (active pane kind) and the
// keys store (region / leader state).
export interface KeyContext {
  region: Region;
  focusedKind: "input" | "terminal" | "other";
  leaderPending: boolean;
  activePaneKind: string | null;
}

export interface Command {
  id: string;
  title: string;
  /** Direct-accelerator chords (raw strings; canonicalized on read), e.g. ["alt+1"]. */
  keys?: string[];
  /** Leader sequence: space-separated keys pressed AFTER the leader, e.g. "p r". The
   * first key is the group id (see Group); a single-key seq is a top-level action. */
  seq?: string;
  /** Gate: the command only matches / shows when this returns true. */
  when?: (ctx: KeyContext) => boolean;
  run: (ctx: KeyContext) => void;
}

export interface Group {
  /** Leader key that opens the group (e.g. "p"). */
  id: string;
  title: string;
}

export interface LeaderChild {
  key: string;
  title: string;
  isGroup: boolean;
}

const ok = (c: Command, ctx: KeyContext): boolean => (c.when ? c.when(ctx) : true);

/** Direct-accelerator lookup: the first available command whose canonical `keys`
 * include the event chord. */
export function matchDirect(commands: Command[], chord: string, ctx: KeyContext): Command | undefined {
  return commands.find((c) => c.keys?.some((k) => canonical(k) === chord) && ok(c, ctx));
}

/** Resolve a full leader path to a runnable command (seq === path, when passes). */
export function resolveLeader(commands: Command[], path: string[], ctx: KeyContext): Command | undefined {
  const seq = path.join(" ");
  return commands.find((c) => c.seq === seq && ok(c, ctx));
}

/** which-key options for the current leader path. path=[] → groups (plus any
 * top-level single-key actions); path=["p"] → that group's actions. Deduped, and a
 * group is shown only when it has at least one available command. */
export function leaderChildren(commands: Command[], groups: Group[], path: string[], ctx: KeyContext): LeaderChild[] {
  const out: LeaderChild[] = [];
  const seen = new Set<string>();
  if (path.length === 0) {
    for (const g of groups) {
      if (commands.some((c) => c.seq?.startsWith(g.id + " ") && ok(c, ctx))) {
        out.push({ key: g.id, title: g.title, isGroup: true });
        seen.add(g.id);
      }
    }
    for (const c of commands) {
      if (c.seq && !c.seq.includes(" ") && ok(c, ctx) && !seen.has(c.seq)) {
        out.push({ key: c.seq, title: c.title, isGroup: false });
        seen.add(c.seq);
      }
    }
    return out;
  }
  const prefix = path.join(" ") + " ";
  for (const c of commands) {
    if (!c.seq || !c.seq.startsWith(prefix) || !ok(c, ctx)) continue;
    const rest = c.seq.slice(prefix.length);
    const next = rest.split(" ")[0];
    if (seen.has(next)) continue;
    seen.add(next);
    const isGroup = rest.includes(" ");
    out.push({ key: next, title: isGroup ? next : c.title, isGroup });
  }
  return out;
}

/** True if `path` is a strict prefix of some available command's seq, so the
 * dispatcher keeps waiting for the next key instead of cancelling. */
export function isLeaderPrefix(commands: Command[], path: string[], ctx: KeyContext): boolean {
  const prefix = path.join(" ") + " ";
  return commands.some((c) => c.seq?.startsWith(prefix) && ok(c, ctx));
}

/** All currently-available commands, for the palette's fuzzy search. */
export function paletteCommands(commands: Command[], ctx: KeyContext): Command[] {
  return commands.filter((c) => ok(c, ctx));
}

/** Apply user key overrides (commandId → chord string; "" = explicitly unbound) onto a
 * command list, returning a new list the dispatcher / overlays can consume unchanged.
 * Only commands that already carry a direct accelerator (`keys`) are overridable — leader
 * SEQUENCES are structural navigation and are left untouched (P5 scope decision, see
 * ADR-0017). Pure: the store read lives in features/keys/bindings.ts. */
export function applyOverrides(commands: Command[], overrides: Record<string, string>): Command[] {
  return commands.map((c) => {
    const o = overrides[c.id];
    if (o === undefined || !c.keys) return c; // no override, or a leader-only command
    return { ...c, keys: o ? [o] : [] }; // "" clears the accelerator (kept reachable via leader)
  });
}
