// The agy popover lists model limits the Agent learned from failed calls (#3744), apart from
// the pools /usage scrapes, and says they are not a scrape.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
}));

import { AgyUsageChip } from "./RemainingUsageChip.tsx";
import { AGY_USAGE } from "./usageSources.ts";
import { setSettings } from "../../lib/settings.ts";

const NOW = Date.parse("2026-11-10T12:00:00Z");
const at = (h: number) => new Date(NOW + h * 3600_000).toISOString();
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function popover(body: unknown): Promise<string> {
  api.mockReset();
  api.mockImplementation(() => Promise.resolve({ kinds: { agy: body } }));
  setSettings({ usageChipsPinned: [], usageChipsFolded: [] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<AgyUsageChip tenant="t" />));
  await act(async () => void (await Promise.resolve()));
  await act(async () => host!.querySelector<HTMLButtonElement>(".ws-usage-btn")?.click());
  return host.querySelector(".ws-usage-pop")?.textContent ?? "";
}

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  localStorage.clear();
});
afterEach(() => {
  if (root) act(() => root!.unmount());
  host?.remove();
  root = host = null;
  vi.useRealTimers();
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

const groups = [{ label: "Gemini", remainingPct: 86, resetsAt: at(100) }];

describe("agy learned limits", () => {
  it("shows the exhausted model beside the scraped pool, labelled as learned", async () => {
    const text = await popover({
      ok: true,
      authed: true,
      groups,
      learned: [{ model: "gemini-4-argon", label: "Gemini 4 Argon", resetsAt: at(50) }],
    });
    expect(text).toContain("Gemini (weekly)");
    expect(text).toContain("Gemini 4 Argon: quota reached");
    expect(text).toMatch(/last failed call/);
    expect(text).toMatch(/Agent restarts/);
  });

  it("names the CLI default for an empty model id", async () => {
    const text = await popover({ ok: true, authed: true, groups, learned: [{ model: "", resetsAt: at(5) }] });
    expect(text).toContain("CLI default: quota reached");
  });

  it("drops a limit whose reset has passed", async () => {
    const text = await popover({ ok: true, authed: true, groups, learned: [{ model: "old", resetsAt: at(-1) }] });
    expect(text).not.toContain("quota reached");
    expect(text).not.toMatch(/failed calls/);
  });

  it("shows a learned limit even when the scrape failed", async () => {
    const text = await popover({ ok: false, authed: true, learned: [{ model: "m1", resetsAt: at(3) }] });
    expect(text).toContain("m1: quota reached");
  });

  it("is a source field, not a chip driver", () => {
    expect(AGY_USAGE.learned?.({ learned: [{ model: "a", resetsAt: at(1) }] }, NOW)).toEqual([{ model: "a", resetsAt: at(1) }]);
  });
});
