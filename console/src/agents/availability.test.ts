import { describe, expect, it } from "vitest";

import { AGENTS, repoLaunchKinds, type AvailCtx } from "./registry.ts";
import { kindDisplayName, kindLabel } from "../lib/sessionkind.ts";

// The launch pickers must never offer an agent the user cannot actually start: the
// session is created, the CLI fails to authenticate, and the slot is wasted. The gate
// used to be `!conns || available({conns})`, whose short-circuit let EVERY kind through
// whenever conns was null — which is the state for the ~1.5-2s the connection check
// takes (it really shells out to `claude auth status` et al.) AND permanently if that
// fetch fails. This pins the predicate half of that gate.
//
// useRepoRail.ts holds the other half: `connsDone && !!conns && available({conns})`.

const ready = (k: string, conns: AvailCtx["conns"]) => !!conns && AGENTS[k as keyof typeof AGENTS].available({ conns });
const gate = (conns: AvailCtx["conns"]) => repoLaunchKinds.filter((k) => ready(k, conns));

describe("launch gate — unknown / failed connection state", () => {
  it("offers NOTHING while the connection state is unknown", () => {
    // The regression: a null conns (in flight, or the fetch errored) must not be read
    // as "everything is fine". shell is included in repoLaunchKinds and its predicate
    // is `() => true`, so this also proves the gate short-circuits BEFORE the predicate.
    expect(gate(null)).toEqual([]);
  });

  it("distinguishes a KNOWN-empty answer from an unknown one", () => {
    // {} is a successful response that happens to list no authenticated agent — unlike
    // null it is an answer, so credential-free shell is correctly still offered.
    expect(gate({})).toEqual(["shell"]);
  });
});

describe("launch gate — per-agent predicates", () => {
  it("offers only the authenticated agents", () => {
    const conns = { claude: { connected: true }, codex: { connected: false }, agy: { connected: false } };
    expect(gate(conns)).toEqual(["claude", "shell"]);
  });

  it("keeps an unauthenticated agy out even though it is installed and supported", () => {
    expect(ready("agy", { agy: { supported: true, connected: false } })).toBe(false);
  });

  it("keeps agy out on a host that cannot run it, even when a token exists", () => {
    // docs/32 Track B RDRAND guard: supported === false hides agy regardless of auth.
    expect(ready("agy", { agy: { supported: false, connected: true } })).toBe(false);
  });

  it("admits agy when supported and authenticated", () => {
    expect(ready("agy", { agy: { supported: true, connected: true } })).toBe(true);
  });

  it("treats an absent `supported` flag as supported (only an explicit false hides)", () => {
    expect(ready("agy", { agy: { connected: true } })).toBe(true);
  });

  it("admits opencode on a configured env even without a connection", () => {
    expect(ready("opencode", { opencode: { envs: ["anthropic"] } })).toBe(true);
    expect(ready("opencode", { opencode: { envs: [] } })).toBe(false);
  });

  it("admits shell once the state is known — it needs no credentials", () => {
    expect(ready("shell", {})).toBe(true);
  });
});

describe("picker display names", () => {
  it("shows agy as its full product name in the launch pickers", () => {
    expect(kindDisplayName("agy")).toBe("Antigravity");
  });

  it("keeps the compact label for chips and headers", () => {
    // The tight spots (LayoutMap, kt-full pane headers) have no room for "Antigravity".
    expect(kindLabel("agy")).toBe("agy");
  });

  it("falls back to label for agents that declare no separate display name", () => {
    for (const k of ["claude", "codex", "opencode", "shell"]) {
      expect(kindDisplayName(k)).toBe(kindLabel(k));
    }
  });
});
