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
    // null it is an answer, so credential-free shell and lcpp (no sign-in exists for this
    // kind either — ADR 0093 決定 10) are correctly still offered.
    expect(gate({})).toEqual(["lcpp", "shell"]);
  });
});

describe("launch gate — per-agent predicates", () => {
  it("offers only the authenticated agents (plus the credential-free ones)", () => {
    const conns = { claude: { connected: true }, codex: { connected: false }, agy: { connected: false } };
    expect(gate(conns)).toEqual(["claude", "lcpp", "shell"]);
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

describe("lcpp — launchable now (ADR 0093 stage 2: driver.go + store.go landed)", () => {
  it("is in repoLaunchKinds like every other CLI-backed kind", () => {
    expect(repoLaunchKinds).toContain("lcpp");
  });

  it("available() has no credential to check (決定 10) — only the on/off switch below gates it", () => {
    expect(AGENTS.lcpp.available({})).toBe(true);
    expect(AGENTS.lcpp.available({ conns: {} })).toBe(true);
    expect(AGENTS.lcpp.available({ conns: { lcpp: { connected: false } } })).toBe(true);
  });

  it("declares the managed-only shape decision 2 describes: managedDriver true, terminalDriver false", () => {
    // managedDriver stays true (the kind's shape is managed-only), and terminalDriver:false
    // says there is no Terminal (CLI) route to fall back to, ever — no CLI program exists to
    // put in a pane (agent.go's BuildLaunch always errors). The two together are what
    // LaunchModal / SessionMenu read to skip offering a driver choice entirely.
    expect(AGENTS.lcpp.managedDriver).toBe(true);
    expect(AGENTS.lcpp.terminalDriver).toBe(false);
  });

  // The managed-only set is small and deliberate, so it is pinned as a SET rather than as a
  // count: terminalDriver:false says a kind has no Terminal (CLI) route at all, ever, and a kind
  // that acquired it by accident would silently stop offering the driver choice. lcpp (ADR 0093)
  // and muse (ADR 0095) are both there because their BuildLaunch always errors — there is no CLI
  // program to put in a pane.
  it("pins the repo-launchable kinds that have no Terminal (CLI) route", () => {
    expect(repoLaunchKinds.filter((k) => AGENTS[k].terminalDriver === false)).toEqual(["lcpp", "muse"]);
  });
});

describe("lcpp — on/off gate (docs/log/105 §106.2, the user's own display setting)", () => {
  it("is admitted when the setting is missing or explicitly true — the opt-out default", () => {
    expect(ready("lcpp", {})).toBe(true);
    expect(ready("lcpp", { lcpp: {} })).toBe(true);
    expect(ready("lcpp", { lcpp: { enabled: true } })).toBe(true);
  });

  it("is hidden only by an explicit enabled:false — this is the signpost half of the gate; the real refusal is server-side (session_handlers.go)", () => {
    expect(ready("lcpp", { lcpp: { enabled: false } })).toBe(false);
  });

  it("does not affect any other kind's availability", () => {
    expect(gate({ lcpp: { enabled: false } })).toEqual(["shell"]);
    expect(gate({ lcpp: { enabled: false }, claude: { connected: true } })).toEqual(["claude", "shell"]);
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

// muse (ADR 0095) — the gate with TWO preconditions, which is what makes it worth its own block.
// Every other agent kind is gated on one thing; muse needs the proprietary binary installed
// (supported) AND a stored credential (connected), and either one missing means a launch could
// only fail: an unauthenticated host accepts session/start and then ends every turn authRequired
// (ADR 0095 P2-1). The server-side gates are HandleCreateSession and the driver's Resume; this
// is only the signpost that keeps the menu honest.
describe("muse — installed AND signed in", () => {
  it("is offered only when both preconditions hold", () => {
    expect(ready("muse", { muse: { supported: true, connected: true } })).toBe(true);
  });

  it("is hidden when the binary is not installed, however good the credential looks", () => {
    expect(ready("muse", { muse: { supported: false, connected: true } })).toBe(false);
  });

  it("is hidden when nobody is signed in, however present the binary is", () => {
    // The one that a `supported !== false` check alone would get wrong — and it is the common
    // state right after the install finishes.
    expect(ready("muse", { muse: { supported: true, connected: false } })).toBe(false);
    expect(ready("muse", { muse: { supported: true } })).toBe(false);
  });

  it("is hidden when the Agent reports nothing about it at all", () => {
    // An older Agent, or a workspace whose /connections has no muse key: absent must read as
    // "cannot launch", not as "no objection". `supported !== false` is true for an absent key,
    // so the connected half is what refuses here.
    expect(ready("muse", {})).toBe(false);
  });
});
