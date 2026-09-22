// The WS bar's usage-chip ranking. What is pinned here is the PRECEDENCE — the four rules
// that decide which agents keep a slot — and the two things that make the ranking usable at
// all: a stamp only ever moves forward (so an archived history still ranks), and the bar
// keeps declaration order (so the chips that stay do not swap places under the pointer).
import { beforeEach, describe, expect, it } from "vitest";
import { AUTO_INLINE, noteSessionKinds, planUsageChips, readKindStamps } from "./usageChipPlan.ts";

// The ranking is pure but its memory is localStorage, and the ranking half of this file is
// pure enough to stay in the fast `node` project (vite.config.js) — so the storage is a
// four-line stand-in rather than a whole jsdom per file.
const store = new Map<string, string>();
globalThis.localStorage = {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => void store.set(k, String(v)),
  removeItem: (k: string) => void store.delete(k),
  clear: () => store.clear(),
  key: (i: number) => [...store.keys()][i] ?? null,
  get length() {
    return store.size;
  },
} as Storage;

const ORDER = ["claude", "codex", "muse", "copilot", "agy"];
const ALL = { order: ORDER, visible: ORDER };
const HOUR = 3600000;

describe("planUsageChips", () => {
  it("keeps the two most recently used and folds the rest, in bar order", () => {
    const now = Date.now();
    const plan = planUsageChips({
      ...ALL,
      stamps: { agy: now, claude: now - HOUR, codex: now - 40 * HOUR, muse: now - 90 * HOUR },
    });
    expect(plan.bar).toEqual(["claude", "agy"]); // ranked by use, EMITTED in declaration order
    expect(plan.fold).toEqual(["codex", "muse", "copilot"]); // most recent first; never-used last
    expect(plan.bar).toHaveLength(AUTO_INLINE);
  });

  it("only places chips that have something to show", () => {
    const plan = planUsageChips({ order: ORDER, visible: ["codex", "agy"], stamps: {} });
    expect(plan.bar).toEqual(["codex", "agy"]);
    expect(plan.fold).toEqual([]);
  });

  it("promotes a near-cap chip onto the bar past the count", () => {
    const now = Date.now();
    const plan = planUsageChips({
      ...ALL,
      urgent: ["muse"],
      stamps: { claude: now, codex: now - HOUR, muse: now - 900 * HOUR },
    });
    expect(plan.bar).toEqual(["claude", "codex", "muse"]);
    expect(plan.fold).not.toContain("muse");
  });

  it("does not widen the bar when the urgent chip was going to be shown anyway", () => {
    const now = Date.now();
    const plan = planUsageChips({
      ...ALL,
      urgent: ["claude"],
      stamps: { claude: now, codex: now - HOUR, muse: now - 900 * HOUR },
    });
    expect(plan.bar).toEqual(["claude", "codex"]);
  });

  it("pins regardless of how long ago the agent was used, and spends the auto budget", () => {
    const now = Date.now();
    const plan = planUsageChips({
      ...ALL,
      pinned: ["copilot"],
      stamps: { claude: now, codex: now - HOUR, copilot: now - 5000 * HOUR },
    });
    expect(plan.bar).toEqual(["claude", "copilot"]);
    expect(plan.fold).toContain("codex");
  });

  it("folds what the user folded even when it is urgent — an explicit answer is final", () => {
    const now = Date.now();
    const plan = planUsageChips({ ...ALL, folded: ["claude"], urgent: ["claude"], stamps: { claude: now } });
    expect(plan.bar).not.toContain("claude");
    expect(plan.fold).toContain("claude");
  });

  it("falls back to declaration order when nothing has been used yet", () => {
    const plan = planUsageChips({ ...ALL, stamps: {} });
    expect(plan.bar).toEqual(["claude", "codex"]);
  });
});

describe("noteSessionKinds", () => {
  beforeEach(() => localStorage.clear());

  it("dates a kind by its newest session and stamps a live one as now", () => {
    const now = 1_700_000_000_000;
    const stamps = noteSessionKinds(
      [
        { kind: "codex", createdAt: new Date(now - 10 * HOUR).toISOString() },
        { kind: "codex", createdAt: new Date(now - 2 * HOUR).toISOString() },
        { kind: "muse", alive: true, createdAt: new Date(now - 900 * HOUR).toISOString() },
      ],
      now,
    );
    expect(stamps.codex).toBe(now - 2 * HOUR);
    expect(stamps.muse).toBe(Math.floor(now / 60000) * 60000); // alive beats its own (ancient) creation date
    expect(readKindStamps()).toEqual(stamps); // and it survives the page
  });

  it("only moves a stamp forward, so an archived history still ranks", () => {
    const now = 1_700_000_000_000;
    noteSessionKinds([{ kind: "claude", createdAt: new Date(now).toISOString() }], now);
    // The claude sessions were cleaned up; the list now holds only a newer codex one.
    const stamps = noteSessionKinds([{ kind: "codex", createdAt: new Date(now + HOUR).toISOString() }], now + HOUR);
    expect(stamps.claude).toBe(now);
    expect(planUsageChips({ ...ALL, stamps }).bar).toEqual(["claude", "codex"]);
  });

  it("ignores rows with no kind or an unparseable date", () => {
    const now = 1_700_000_000_000;
    const stamps = noteSessionKinds([{ createdAt: "x" }, { kind: "agy", createdAt: "not-a-date" }, { kind: "" }], now);
    expect(stamps).toEqual({});
  });

  it("quantises a live session's stamp to the minute (the 4s poll must not rewrite storage)", () => {
    const base = 1_700_000_000_000;
    const a = noteSessionKinds([{ kind: "claude", alive: true }], base + 1000);
    const b = noteSessionKinds([{ kind: "claude", alive: true }], base + 5000);
    expect(b.claude).toBe(a.claude);
  });
});
