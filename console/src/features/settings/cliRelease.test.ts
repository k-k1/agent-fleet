import { describe, it, expect } from "vitest";
import { watcherHealth, WATCHER_STALE_MS, type CLIRelease } from "./cliRelease.ts";

const NOW = Date.parse("2026-09-11T06:00:00Z");
const iso = (msAgo: number) => new Date(NOW - msAgo).toISOString();

const state = (p: Partial<CLIRelease> = {}): CLIRelease => ({
  watcherOkAt: iso(3600_000),
  watcherFailed: [],
  watcherAt: iso(3600_000),
  tested: { claude: "2.1.267" },
  fetchedAt: iso(0),
  ...p,
});

describe("watcherHealth", () => {
  it("is unknown when the CP never got an answer (no outbound network)", () => {
    expect(watcherHealth(null, NOW).kind).toBe("unknown");
    expect(watcherHealth(undefined, NOW).kind).toBe("unknown");
  });

  it("is unknown when the issue body held nothing readable — not 'fine', not a warning", () => {
    expect(watcherHealth(state({ watcherOkAt: "", watcherAt: "" }), NOW).kind).toBe("unknown");
    expect(watcherHealth(state({ watcherOkAt: "yesterday-ish" }), NOW).kind).toBe("unknown");
  });

  it("is ok for a recent clean run", () => {
    expect(watcherHealth(state(), NOW).kind).toBe("ok");
  });

  it("is failed when rows could not be read, naming them", () => {
    const h = watcherHealth(state({ watcherFailed: ["rtk", "kiro"] }), NOW);
    expect(h.kind).toBe("failed");
    expect(h.kind === "failed" && h.rows).toEqual(["rtk", "kiro"]);
  });

  it("reports failed rows even when the watcher has never had a clean run", () => {
    const h = watcherHealth(state({ watcherOkAt: "", watcherFailed: ["rtk"] }), NOW);
    expect(h.kind).toBe("failed");
    expect(h.kind === "failed" && isNaN(h.okAt)).toBe(true);
  });

  it("is stale past 48h, and not one second before", () => {
    expect(watcherHealth(state({ watcherOkAt: iso(WATCHER_STALE_MS - 1000) }), NOW).kind).toBe("ok");
    expect(watcherHealth(state({ watcherOkAt: iso(WATCHER_STALE_MS + 1000) }), NOW).kind).toBe("stale");
  });

  it("ignores the empty strings a shell list leaves behind", () => {
    expect(watcherHealth(state({ watcherFailed: ["", ""] }), NOW).kind).toBe("ok");
  });
});
