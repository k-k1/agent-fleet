// Equivalence proof for RemainingUsageChip (#2891): it replaced AgyUsageChip and
// CopilotUsageChip, which were two copies of one chip. This drives every implementation through
// the same matrix — each reading shape, bare and inside the fold group, bar and folded, the
// dropdown opened by click and by keyboard, refresh, dismiss — and records the rendered HTML, the
// layout reports the fold group hears and the endpoint calls after every step. The golden holds
// what the old chips rendered, so the merged chip is held to them byte for byte.
//
// Regenerate (only when a visible change is intended, and say so in the commit):
//   UPDATE_REMAINING_CHIP_GOLDEN=1 npx vitest run src/app/wsbar/RemainingUsageChip.dom.test.tsx
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
}));
// Absolute times would otherwise render in the machine's time zone and make the golden
// host-dependent. Every implementation goes through the same formatter, so pinning it hides no
// difference between them.
vi.mock("../../lib/intl.ts", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../../lib/intl.ts")>();
  return {
    ...orig,
    fmtDateTime: (when: string | number | Date, opts?: Intl.DateTimeFormatOptions) => {
      const ms = new Date(when).getTime();
      if (isNaN(ms)) return typeof when === "string" ? when : "";
      return new Date(ms).toISOString() + (opts === orig.TIME_HM ? " hm" : "");
    },
  };
});

import { AgyUsageChip } from "./AgyUsageChip.tsx";
import { CopilotUsageChip } from "./CopilotUsageChip.tsx";
import { RemainingUsageChip } from "./RemainingUsageChip.tsx";
import { AGY_USAGE, COPILOT_USAGE, type RemainingSource } from "./usageSources.ts";
import { UsageChipFold } from "./usageFold.tsx";
import { useSessionsStore } from "../../features/sessions/store.ts";
import { useUiOpen } from "../../core/store/uiOpen.ts";
import { setSettings } from "../../lib/settings.ts";

const GOLDEN = path.resolve(__dirname, "remainingUsageChip.golden.json");
const UPDATE = process.env.UPDATE_REMAINING_CHIP_GOLDEN === "1";

// Noon UTC: a reset 20 minutes out is the same local day in every time zone, so the chip's
// "same day → HH:MM only" rule does not depend on the host either.
const NOW = Date.parse("2026-06-15T12:00:00Z");
const MIN = 60000;
const at = (ms: number) => new Date(NOW + ms).toISOString();
const SOON = at(20 * MIN);
const LATER = at(3 * 24 * 60 * MIN);
const PAST = at(-30 * MIN);

type Reply = { kind: "ok"; body: unknown } | { kind: "pending" } | { kind: "reject" };
const ok = (body: unknown): Reply => ({ kind: "ok", body });
const COMMON: Record<string, Reply> = {
  loading: { kind: "pending" },
  error: { kind: "reject" },
  "signed-out": ok({ ok: false, authed: false }),
  "authed-unavailable": ok({ ok: false, authed: true, plan: "pro", account: "a@x", user: "u1" }),
  "empty-pools": ok({ ok: true, authed: true, groups: [], quotas: [], ageSec: 5 }),
};

const AGY_CASES: Record<string, Reply> = {
  ...COMMON,
  "one-pool": ok({ ok: true, authed: true, groups: [{ label: "Gemini", remainingPct: 99, resetsAt: LATER }], ageSec: 30 }),
  "several-pools": ok({
    ok: true,
    authed: true,
    account: "a@example.com",
    plan: "Pro",
    groups: [
      { label: "Gemini", remainingPct: 60, resetsAt: LATER, fiveHour: { remainingPct: 15, resetsAt: SOON } },
      { label: "Claude+GPT", remainingPct: 40, resetsAt: LATER, fiveHour: { remainingPct: 90, resetsAt: SOON } },
    ],
    ageSec: 4000,
  }),
  "near-cap": ok({
    ok: true,
    authed: true,
    groups: [{ label: "Gemini", remainingPct: 50, resetsAt: LATER, fiveHour: { remainingPct: 3, resetsAt: SOON } }],
    ageSec: 90,
  }),
  "over-cap-both": ok({
    ok: true,
    authed: true,
    groups: [{ label: "Gemini", remainingPct: 0, resetsAt: LATER, fiveHour: { remainingPct: -5, resetsAt: SOON } }],
    ageSec: 90,
  }),
  "cap-second-group": ok({
    ok: true,
    authed: true,
    groups: [
      { label: "Gemini", remainingPct: 80 },
      { label: "Claude+GPT", remainingPct: 1, resetsAt: SOON },
    ],
  }),
  "cap-reset-passed": ok({ ok: true, authed: true, groups: [{ label: "Gemini", remainingPct: 0, resetsAt: PAST }], ageSec: 30 }),
  "missing-resets": ok({
    ok: true,
    authed: true,
    groups: [{ label: "Gemini", remainingPct: 2, fiveHour: { remainingPct: 1 } }],
    ageSec: 30,
  }),
  stale: ok({
    ok: true,
    authed: true,
    groups: [{ label: "Gemini", remainingPct: 0, resetsAt: SOON, stale: true, fiveHour: { remainingPct: 0, resetsAt: SOON, stale: true } }],
    ageSec: 30,
  }),
  "stale-five-only": ok({
    ok: true,
    authed: true,
    groups: [{ label: "Gemini", remainingPct: 70, resetsAt: LATER, fiveHour: { remainingPct: 0, resetsAt: SOON, stale: true } }],
  }),
};

const COPILOT_CASES: Record<string, Reply> = {
  ...COMMON,
  "one-pool": ok({ ok: true, authed: true, quotas: [{ id: "chat", remainingPct: 98 }], ageSec: 30 }),
  "several-pools": ok({
    ok: true,
    authed: true,
    user: "octo",
    plan: "individual",
    canUpgrade: true,
    resetsAt: LATER,
    quotas: [
      { id: "premium_interactions", remainingPct: 40 },
      { id: "chat", remainingPct: 100 },
      { id: "completions", remainingPct: 75 },
      { id: "mystery_pool", remainingPct: 10 },
    ],
    ageSec: 120,
  }),
  "plan-no-upgrade": ok({ ok: true, authed: true, plan: "business", canUpgrade: false, resetsAt: LATER, quotas: [{ id: "chat", remainingPct: 50 }] }),
  "near-cap": ok({ ok: true, authed: true, resetsAt: SOON, quotas: [{ id: "premium_interactions", remainingPct: 4 }, { id: "chat", remainingPct: 90 }] }),
  "over-cap": ok({ ok: true, authed: true, resetsAt: LATER, quotas: [{ id: "premium_interactions", remainingPct: -3 }] }),
  "cap-reset-passed": ok({ ok: true, authed: true, resetsAt: PAST, quotas: [{ id: "chat", remainingPct: 0 }] }),
  "missing-resets": ok({ ok: true, authed: true, quotas: [{ id: "chat", remainingPct: 0 }, { id: "completions", remainingPct: 60 }], ageSec: 10 }),
};

// The implementations under comparison, per source. `kind` is what the fold settings and the
// keyboard address the chip by.
interface Impl {
  name: string;
  make: () => ReactElement;
}
const SOURCES: { src: RemainingSource; cases: Record<string, Reply>; impls: Impl[] }[] = [
  {
    src: COPILOT_USAGE,
    cases: COPILOT_CASES,
    impls: [
      { name: "old", make: () => <CopilotUsageChip tenant="t" /> },
      { name: "new", make: () => <RemainingUsageChip src={COPILOT_USAGE} tenant="t" /> },
    ],
  },
  {
    src: AGY_USAGE,
    cases: AGY_CASES,
    impls: [
      { name: "old", make: () => <AgyUsageChip tenant="t" /> },
      { name: "new", make: () => <RemainingUsageChip src={AGY_USAGE} tenant="t" /> },
    ],
  },
];

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement | null = null;

function answer(reply: Reply, refreshed: boolean) {
  if (reply.kind === "pending") return new Promise(() => {});
  if (reply.kind === "reject") return Promise.reject(new Error("boom"));
  // A manual refresh answers a newer reading, so the trace shows the chip adopting it.
  if (refreshed && reply.body && typeof reply.body === "object")
    return Promise.resolve({ ...(reply.body as object), ageSec: 0 });
  return Promise.resolve(reply.body);
}

const flush = async () => {
  await act(async () => void (await Promise.resolve()));
  await act(async () => void (await Promise.resolve()));
};

type Step = { step: string; html: string; layout: string | null; calls: string[] };

// On disk each distinct HTML string is stored once and the steps point into that table: the
// same markup recurs across modes and steps, and inline it is 650 KB.
type Golden = { html: string[]; cases: Record<string, (Omit<Step, "html"> & { html: number })[]> };
function pack(runs: Record<string, Step[]>): Golden {
  const html: string[] = [];
  const idx = (h: string) => (html.includes(h) ? html.indexOf(h) : html.push(h) - 1);
  const cases: Golden["cases"] = {};
  for (const [id, steps] of Object.entries(runs)) cases[id] = steps.map((s) => ({ ...s, html: idx(s.html) }));
  return { html, cases };
}
function unpack(g: Golden, id: string): Step[] {
  return g.cases[id].map((s) => ({ ...s, html: g.html[s.html] }));
}

// One run: mount the chip, then walk it through the interactions, recording after each.
async function trace(src: RemainingSource, reply: Reply, impl: Impl, mode: "bare" | "group" | "folded"): Promise<Step[]> {
  api.mockReset();
  api.mockImplementation((p: string) => answer(reply, p.includes("refresh=1")));
  setSettings({ usageChipsPinned: [], usageChipsFolded: mode === "folded" ? [src.kind] : [] });
  const layouts: string[] = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const steps: Step[] = [];
  // Only the chip's own endpoint: setSettings' ui-prefs sync lands on its own timer.
  const snap = (step: string) =>
    steps.push({
      step,
      html: host!.innerHTML,
      layout: layouts.at(-1) ?? null,
      calls: api.mock.calls.map((c) => String(c[0])).filter((c) => c.startsWith(src.endpoint)),
    });
  const chipBtn = () => host!.querySelector<HTMLButtonElement>(".ws-usage-btn");
  const click = async (el: Element | null | undefined) => {
    if (!el) return snap("(missing)");
    await act(async () => (el as HTMLElement).click());
    await flush();
  };

  await act(async () => {
    root!.render(mode === "bare" ? impl.make() : <UsageChipFold onLayoutChange={(k) => void layouts.push(k)}>{impl.make()}</UsageChipFold>);
  });
  await flush();
  snap("mount");
  if (mode === "folded") {
    await click(host.querySelector(".ws-fold-btn"));
    snap("fold-open");
  }
  await click(chipBtn());
  snap("chip-click");
  await click(host.querySelector(".wu-reload"));
  snap("refresh");
  await act(async () => {
    document.body.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    document.body.dispatchEvent(new MouseEvent("pointerup", { bubbles: true }));
    document.body.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    document.body.click();
  });
  await flush();
  snap("dismiss");
  if (mode === "folded") {
    await click(host.querySelector(".ws-fold-btn")); // close the +N
    snap("fold-close");
  }
  await act(async () => useUiOpen.getState().toggle(src.key));
  await flush();
  snap("key-toggle");
  await act(async () => useUiOpen.getState().toggle(src.key));
  await flush();
  snap("key-toggle-again");

  act(() => root!.unmount());
  host.remove();
  root = null;
  host = null;
  return steps;
}

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  localStorage.clear();
  useSessionsStore.setState({ sessions: [] });
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.useRealTimers();
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("RemainingUsageChip: equivalence with the chips it replaced", () => {
  // Filled case by case below, then held to the golden as a whole; tests in a file run in order.
  const got: Record<string, Step[]> = {};
  for (const { src, cases, impls } of SOURCES) {
    for (const [caseName, reply] of Object.entries(cases)) {
      for (const mode of ["bare", "group", "folded"] as const) {
        const id = `${src.kind}/${caseName}/${mode}`;
        it(`renders ${id} the same in every implementation`, async () => {
          const runs = [];
          for (const impl of impls) runs.push({ impl: impl.name, steps: await trace(src, reply, impl, mode) });
          for (const r of runs.slice(1)) expect(r.steps, `${r.impl} vs ${runs[0].impl}`).toEqual(runs[0].steps);
          got[id] = runs[0].steps;
        });
      }
    }
  }

  it("matches the golden", () => {
    const fresh = pack(got);
    if (UPDATE) writeFileSync(GOLDEN, JSON.stringify(fresh, null, 1) + "\n");
    expect(existsSync(GOLDEN), `no golden; regenerate it: UPDATE_REMAINING_CHIP_GOLDEN=1`).toBe(true);
    const golden: Golden = JSON.parse(readFileSync(GOLDEN, "utf8"));
    expect(Object.keys(fresh.cases)).toEqual(Object.keys(golden.cases));
    for (const id of Object.keys(golden.cases)) expect(unpack(fresh, id), id).toEqual(unpack(golden, id));
  });

  it("is not vacuous: the matrix reaches every part of the chip", () => {
    const golden: Golden = JSON.parse(readFileSync(GOLDEN, "utf8"));
    const all = golden.html.join("\n");
    // Each of these is a branch some case has to have rendered for the comparison to cover it.
    for (const needle of [
      "ws-usage-nums crit", // near cap
      "ws-usage-nums muted", // unavailable
      "wu-pct muted", // a stale row
      "wu-pct crit",
      "wu-pct warn",
      "wu-place",
      "ws-fold-list",
      "github.com/settings/copilot",
      " · upgradable", // copilot's plan line suffix
      "Experimental slot", // agy's note
      'aria-expanded="true"',
    ])
      expect(all, needle).toContain(needle);
    const ids = Object.keys(golden.cases);
    expect(ids.filter((k) => golden.html[golden.cases[k][0].html] === "").length, "a hidden chip").toBeGreaterThan(0);
    expect(ids.length).toBe((Object.keys(AGY_CASES).length + Object.keys(COPILOT_CASES).length) * 3);
  });
});
