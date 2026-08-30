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
    // docs/log/32 Track B RDRAND guard: supported === false hides agy regardless of auth.
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

  it("keeps opencode out while usage is explicitly off, even with a key or OAuth still present", () => {
    // usage:"off" is a tamper-resistant hard disable (docs/log/54 §… — same override the
    // Agent applies in opencode.Connected()) — a stray stored key must not re-admit it.
    expect(ready("opencode", { opencode: { usage: "off", envs: ["anthropic"] } })).toBe(false);
    expect(ready("opencode", { opencode: { usage: "off", connected: true } })).toBe(false);
  });

  it("admits shell once the state is known — it needs no credentials", () => {
    expect(ready("shell", {})).toBe(true);
  });
});

describe("repo launch menu", () => {
  it("offers only kinds that carry the launchableFromRepo cap (registry.ts contract)", () => {
    // repoLaunchKinds is a hand-ordered list next to the registry — pin the cap so a
    // future kind can't be added to the menu without actually being repo-launchable.
    expect(repoLaunchKinds.filter((k) => !AGENTS[k].caps.launchableFromRepo)).toEqual([]);
  });
});

describe("display names", () => {
  it("uses the full product name in the launch pickers where it differs from the label", () => {
    expect(kindDisplayName("claude")).toBe("Claude Code");
    expect(kindDisplayName("copilot")).toBe("GitHub Copilot");
    expect(kindDisplayName("agy")).toBe("Antigravity");
  });

  it("keeps a proper-cased compact label for chips and headers", () => {
    // The tight spots (LayoutMap, pane headers) show the compact label, not the full name.
    expect(kindLabel("claude")).toBe("Claude");
    expect(kindLabel("copilot")).toBe("Copilot");
    expect(kindLabel("codex")).toBe("Codex");
    expect(kindLabel("opencode")).toBe("OpenCode");
    expect(kindLabel("agy")).toBe("Antigravity");
  });

  it("falls back to the label when an agent declares no separate display name", () => {
    // codex / opencode / agy: full name == compact label, so displayName is the label.
    for (const k of ["codex", "opencode", "shell"]) {
      expect(kindDisplayName(k)).toBe(kindLabel(k));
    }
  });
});
