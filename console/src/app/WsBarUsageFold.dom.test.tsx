// Folding the WS bar's agent-usage chips (app/usageChipPlan.ts + WsBar's UsageChipFold).
//
// The ranking itself is covered by usageChipPlan.test.ts. What needs a real tree is the
// wiring the ranking depends on, and it is exactly the part that cannot be reasoned about
// from the plan:
//   1. A chip decides for ITSELF whether it has anything to show (its endpoint may say
//      "not signed in"), so the group can only count chips that reported. A chip whose
//      report never arrives would be counted as folded and inflate "+N".
//   2. A folded chip is MOVED, not unmounted: it portals into the popover keeping its
//      reading and its poll. If folding unmounted it, the group could not know a hidden
//      chip had gone near its cap — and every open would cost a fresh fetch per agent.
//   3. Near the cap the chip stops showing a percentage and shows when you are unblocked,
//      and that has to reach the bar even for an agent nobody has run lately.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
}));
// Reset notifications subscribe to their own timers and toasts; the chips' placement is
// what is under test.
vi.mock("./usageResetNotify.ts", () => ({ useUsageResetNotify: () => {} }));

import { AgyUsageChip, CopilotUsageChip, UsageChip, UsageChipFold, USAGE_SOURCES } from "./WsBar.tsx";
import { useSessionsStore } from "../features/sessions/store.ts";
import { useUiOpen } from "../core/store/uiOpen.ts";
import { setSettings } from "../lib/settings.ts";
import type { Session } from "../types/session.ts";

const HOUR = 3600000;
const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString();

// A signed-in agent well below its cap.
const calm = (five: number, week: number) => ({
  ok: true,
  authed: true,
  fiveHour: { pct: five, resetsAt: new Date(Date.now() + 2 * HOUR).toISOString() },
  sevenDay: { pct: week, resetsAt: new Date(Date.now() + 40 * HOUR).toISOString() },
  ageSec: 30,
});
// Signed out: the chip has nothing to show and never will, so it reports invisible.
const signedOut = { ok: false, authed: false };

// React only recognises act() when the environment says so (the same flag RepoRow.dom.test
// sets); without it every act() prints a warning and its effects are not flushed in order.
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function bar() {
  return [...host!.querySelectorAll(".ws-usage-wrap:not(.ws-fold) .ws-usage-btn")]
    .filter((b) => !b.closest(".ws-fold-list"))
    .map((b) => b.querySelector(".ws-usage-name")!.textContent);
}
function folded() {
  return [...host!.querySelectorAll(".ws-fold-list .ws-usage-name")].map((n) => n.textContent);
}
function foldBtn() {
  return host!.querySelector<HTMLButtonElement>(".ws-fold-btn");
}

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <UsageChipFold>
        <>
          {USAGE_SOURCES.map((s) => (
            <UsageChip key={s.endpoint} src={s} tenant="t" />
          ))}
          <CopilotUsageChip tenant="t" />
          <AgyUsageChip tenant="t" />
        </>
      </UsageChipFold>,
    );
  });
  await act(async () => void (await Promise.resolve()));
}

// Sessions are the ranking's input: a live session means "in use now", otherwise the newest
// session of that kind dates the agent.
function seedSessions(rows: { kind: string; alive?: boolean; agoMs?: number }[]) {
  useSessionsStore.setState({
    sessions: rows.map((r, i) => ({
      name: `s${i}`,
      kind: r.kind,
      alive: r.alive,
      createdAt: iso(r.agoMs ?? 0),
    })) as Session[],
  });
}

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  localStorage.clear();
  setSettings({ usageChipsPinned: [], usageChipsFolded: [] });
  api.mockReset();
  api.mockImplementation((path: string) => {
    if (path.startsWith("api/claude/usage")) return Promise.resolve(calm(14, 70));
    if (path.startsWith("api/codex/usage")) return Promise.resolve(calm(33, 27));
    if (path.startsWith("api/muse/usage")) return Promise.resolve(calm(1, 2));
    if (path.startsWith("api/copilot/usage"))
      return Promise.resolve({ ok: true, authed: true, quotas: [{ id: "chat", remainingPct: 98 }], ageSec: 30 });
    if (path.startsWith("api/connections/agy/usage"))
      return Promise.resolve({ ok: true, authed: true, groups: [{ label: "Gemini", remainingPct: 99 }], ageSec: 30 });
    return Promise.resolve({});
  });
  seedSessions([
    { kind: "claude", alive: true },
    { kind: "codex", agoMs: 2 * HOUR },
    { kind: "muse", agoMs: 400 * HOUR },
    { kind: "copilot", agoMs: 900 * HOUR },
    { kind: "agy", agoMs: 1000 * HOUR },
  ]);
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  useSessionsStore.setState({ sessions: [] });
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("WS bar usage chips: folding", () => {
  it("keeps the two most recently used on the bar and folds the other three", async () => {
    await mount();
    expect(bar()).toEqual(["Claude", "Codex"]);
    expect(foldBtn()!.textContent).toContain("+3");
    // Folded chips are not rendered anywhere until the popover is opened...
    expect(folded()).toEqual([]);
  });

  it("moves the folded chips into the popover without re-fetching them", async () => {
    await mount();
    const callsBefore = api.mock.calls.length;
    await act(async () => foldBtn()!.click());
    expect(folded()).toEqual(["Muse", "Copilot", "Antigravity"]);
    expect(bar()).toEqual(["Claude", "Codex"]); // and they did not leave the bar
    expect(api.mock.calls.length).toBe(callsBefore); // moved, not re-mounted
  });

  it("leaves a signed-out agent out of the count entirely", async () => {
    api.mockImplementation((path: string) => {
      if (path.startsWith("api/claude/usage")) return Promise.resolve(calm(14, 70));
      if (path.startsWith("api/codex/usage")) return Promise.resolve(calm(33, 27));
      if (path.startsWith("api/muse/usage")) return Promise.resolve(calm(1, 2));
      return Promise.resolve(signedOut); // copilot + agy: never signed in
    });
    await mount();
    expect(bar()).toEqual(["Claude", "Codex"]);
    expect(foldBtn()!.textContent).toContain("+1");
    await act(async () => foldBtn()!.click());
    expect(folded()).toEqual(["Muse"]);
  });

  it("promotes a near-cap agent onto the bar however long ago it was used", async () => {
    api.mockImplementation((path: string) => {
      if (path.startsWith("api/claude/usage")) return Promise.resolve(calm(14, 70));
      if (path.startsWith("api/codex/usage")) return Promise.resolve(calm(33, 27));
      if (path.startsWith("api/muse/usage")) return Promise.resolve(calm(97, 12)); // blocked
      return Promise.resolve(signedOut);
    });
    await mount();
    expect(bar()).toEqual(["Claude", "Codex", "Muse"]);
    expect(foldBtn()).toBeNull(); // nothing left to fold
  });

  it("honours an explicit fold over both the ranking and the near-cap promotion", async () => {
    api.mockImplementation((path: string) => {
      if (path.startsWith("api/claude/usage")) return Promise.resolve(calm(99, 70)); // blocked
      if (path.startsWith("api/codex/usage")) return Promise.resolve(calm(33, 27));
      if (path.startsWith("api/muse/usage")) return Promise.resolve(calm(1, 2));
      return Promise.resolve(signedOut);
    });
    setSettings({ usageChipsFolded: ["claude"] });
    await mount();
    expect(bar()).toEqual(["Codex", "Muse"]);
    await act(async () => foldBtn()!.click());
    expect(folded()).toEqual(["Claude"]);
  });

  it("opens the popover when a FOLDED chip is toggled from the keyboard", async () => {
    await mount();
    // Ctrl/⌘+K g … targets a chip that is currently inside the fold: without revealing it,
    // the shortcut would render nothing at all for exactly the agents it is most useful for.
    await act(async () => useUiOpen.getState().toggle("usage-muse"));
    expect(folded()).toContain("Muse");
  });

  it("does not open the popover for a chip that is already on the bar", async () => {
    await mount();
    await act(async () => useUiOpen.getState().toggle("usage-claude"));
    expect(host!.querySelector(".ws-fold-pop")).toBeNull();
  });

  it("honours a pin on an agent nobody has run lately", async () => {
    setSettings({ usageChipsPinned: ["agy"] });
    await mount();
    expect(bar()).toEqual(["Claude", "Antigravity"]); // the pin spends one of the two slots
    expect(foldBtn()!.textContent).toContain("+3");
  });
});
