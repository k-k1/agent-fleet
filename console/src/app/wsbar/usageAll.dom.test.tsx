// The WS bar's usage chips share one GET /usage/all per poll (#3127). What needs a real tree:
// every chip mounts in the same commit and must fold into ONE request, a chip's reload must
// force only its own kind, and against an Agent that has no /usage/all yet each chip must
// fall back to its own endpoint rather than go blank.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
}));
vi.mock("../usageResetNotify.ts", () => ({ useUsageResetNotify: () => {} }));

import { UsageChip } from "./UsageChip.tsx";
import { CopilotUsageChip } from "./RemainingUsageChip.tsx";
import { USAGE_SOURCES } from "./usageSources.ts";
import { refreshUsage, subscribeUsage, USAGE_ALL } from "./usageAll.ts";

const HOUR = 3600000;
const calm = (five: number, week: number) => ({
  ok: true,
  authed: true,
  fiveHour: { pct: five, resetsAt: new Date(Date.now() + 2 * HOUR).toISOString() },
  sevenDay: { pct: week, resetsAt: new Date(Date.now() + 40 * HOUR).toISOString() },
  ageSec: 30,
});
const copilot = { ok: true, authed: true, quotas: [{ id: "chat", remainingPct: 75 }], ageSec: 30 };

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement | null = null;

const claudeSrc = USAGE_SOURCES.find((s) => s.kind === "claude")!;
const museSrc = USAGE_SOURCES.find((s) => s.kind === "muse")!;

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => void (await Promise.resolve()));
}

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <>
        <UsageChip src={claudeSrc} tenant="t" />
        <UsageChip src={museSrc} tenant="t" />
        <CopilotUsageChip tenant="t" />
      </>,
    );
  });
  await settle();
}

const nums = () => [...host!.querySelectorAll(".ws-usage-nums")].map((n) => n.textContent);
const calls = () => api.mock.calls.map((c) => String(c[0]));

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("usage chips: one shared poll", () => {
  it("reads every chip from a single /usage/all", async () => {
    api.mockImplementation((p: string) =>
      p.startsWith(USAGE_ALL)
        ? Promise.resolve({ kinds: { claude: calm(14, 70), muse: calm(7, 29), copilot } })
        : Promise.reject(new Error("per-kind endpoint must not be called")),
    );
    await mount();
    expect(calls()).toEqual([USAGE_ALL]);
    expect(nums()).toEqual(["14% / 70%", "7% / 29%", "25%"]);
  });

  it("forces only the chip's own kind on reload", async () => {
    // claude's chip has the reload button (muse and codex are snapshots with nothing to refresh
    // towards); the forced answer carries a newer claude reading.
    api.mockImplementation((p: string) =>
      Promise.resolve({
        kinds: { claude: p.includes("kind=claude") ? calm(20, 71) : calm(14, 70), muse: calm(7, 29), copilot },
      }),
    );
    await mount();
    await act(async () => host!.querySelectorAll<HTMLButtonElement>(".ws-usage-btn")[0].click());
    await act(async () => host!.querySelector<HTMLButtonElement>(".wu-reload")!.click());
    await settle();
    expect(calls()).toEqual([USAGE_ALL, `${USAGE_ALL}?refresh=1&kind=claude`]);
    expect(nums()[0]).toBe("20% / 71%");
  });

  // api() resolves an HTTP error as {error}; only a network failure rejects. An Agent older than
  // this Console answers /usage/all with a 404, i.e. the resolved shape.
  for (const [name, answer] of [
    ["a 404 (resolved as {error})", () => Promise.resolve({ error: { code: "http_404", message: "404 page not found" } })],
    ["a network failure (rejected)", () => Promise.reject(new Error("offline"))],
  ] as const) {
    it(`falls back to each chip's own endpoint on ${name}`, async () => {
      api.mockImplementation((p: string) => {
        if (p.startsWith(USAGE_ALL)) return answer();
        if (p.startsWith("api/claude/usage")) return Promise.resolve(calm(14, 70));
        if (p.startsWith("api/muse/usage")) return Promise.resolve(calm(7, 29));
        if (p.startsWith("api/copilot/usage")) return Promise.resolve(copilot);
        return Promise.resolve({});
      });
      await mount();
      expect(calls().sort()).toEqual([USAGE_ALL, "api/claude/usage", "api/copilot/usage", "api/muse/usage"].sort());
      expect(nums()).toEqual(["14% / 70%", "7% / 29%", "25%"]);
      // The reload falls back too, and reads only its own chip.
      api.mockClear();
      await act(async () => host!.querySelectorAll<HTMLButtonElement>(".ws-usage-btn")[0].click());
      await act(async () => host!.querySelector<HTMLButtonElement>(".wu-reload")!.click());
      await settle();
      expect(calls()).toEqual([`${USAGE_ALL}?refresh=1&kind=claude`, "api/claude/usage?refresh=1"]);
    });
  }

  it("asks again shortly for the kinds the Agent answered as pending", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "setInterval"] });
    try {
      let n = 0;
      api.mockImplementation(() =>
        Promise.resolve(
          n++ === 0
            ? { kinds: { claude: calm(14, 70), muse: calm(7, 29) }, pending: ["copilot"] }
            : { kinds: { claude: calm(14, 70), muse: calm(7, 29), copilot } },
        ),
      );
      await mount();
      expect(nums()).toEqual(["14% / 70%", "7% / 29%"]); // copilot still reading: not shown yet
      await act(async () => void vi.advanceTimersByTime(5000));
      await settle();
      expect(calls()).toEqual([USAGE_ALL, USAGE_ALL]);
      expect(nums()).toEqual(["14% / 70%", "7% / 29%", "25%"]);
      // Nothing pending any more: no further follow-up.
      await act(async () => void vi.advanceTimersByTime(60000));
      expect(calls()).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
  });

  // Every delivery is a new object to a chip, and claude/codex/muse chips post a usage
  // observation on each: a reload of one chip must not make the others post.
  it("delivers a refresh only to the chip that asked", async () => {
    api.mockImplementation(() => Promise.resolve({ kinds: { claude: calm(14, 70), muse: calm(7, 29) } }));
    const got: string[] = [];
    const offC = subscribeUsage("t2", "claude", "api/claude/usage", () => got.push("claude"));
    const offM = subscribeUsage("t2", "muse", "api/muse/usage", () => got.push("muse"));
    try {
      await settle();
      expect(got.sort()).toEqual(["claude", "muse"]);
      got.length = 0;
      await refreshUsage("t2", "claude");
      expect(got).toEqual(["claude"]);
    } finally {
      offC();
      offM();
    }
  });

  it("keeps a chip's reading when the kind is missing from a later answer", async () => {
    let n = 0;
    api.mockImplementation(() =>
      Promise.resolve(n++ === 0 ? { kinds: { claude: calm(14, 70), muse: calm(7, 29), copilot } } : { kinds: { claude: calm(15, 70) } }),
    );
    await mount();
    await act(async () => host!.querySelectorAll<HTMLButtonElement>(".ws-usage-btn")[0].click());
    await act(async () => host!.querySelector<HTMLButtonElement>(".wu-reload")!.click());
    await settle();
    expect(nums()).toEqual(["15% / 70%", "7% / 29%", "25%"]);
  });
});
