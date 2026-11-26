import { describe, expect, it } from "vitest";
import {
  chatOffered,
  defaultRouteUsable,
  emptyRouteOk,
  kindLaunchable,
  mergeVisibleOrder,
  quickLaunchRoute,
  routelessOk,
  type LiveKindAccess,
} from "./launchAccess.ts";

// ADR 0138 D10a / D11b over the live, policy-filtered kind catalogue: what a launch menu offers,
// and when a launch may leave its route unchosen.

const live = (...ks: LiveKindAccess[]) => new Map(ks.map((k) => [k.kind, k]));
const claude = (routes: string[], usableRoutes?: string[]): LiveKindAccess => ({ kind: "claude", routes, ...(usableRoutes ? { usableRoutes } : {}) });

describe("kindLaunchable", () => {
  it("offers nothing that runs a model without a confirmed catalogue", () => {
    expect(kindLaunchable("claude", { conns: { claude: { connected: true } } }, null)).toBe(false);
    // A shell runs no model, so an unknown policy does not take it away.
    expect(kindLaunchable("shell", { conns: {} }, null)).toBe(true);
  });

  it("offers no kind the catalogue leaves out (forbidden, or switched off by the member)", () => {
    const conns = { claude: { connected: true }, codex: { connected: true } };
    expect(kindLaunchable("codex", { conns }, live(claude(["anthropic-subscription"])))).toBe(false);
  });

  it("offers claude on an Anthropic key alone (no subscription login)", () => {
    const l = live(claude(["anthropic-api", "anthropic-subscription"], ["anthropic-api"]));
    expect(kindLaunchable("claude", { conns: { claude: { connected: false } } }, l)).toBe(true);
  });

  it("does not offer claude with no usable route, even when its login is connected", () => {
    const l = live(claude(["anthropic-api"], []));
    expect(kindLaunchable("claude", { conns: { claude: { connected: true } } }, l)).toBe(false);
  });

  it("keeps another kind's own connection check", () => {
    const codex: LiveKindAccess = { kind: "codex", fixedRoutes: ["openai-api"], usableRoutes: ["openai-api"] };
    expect(kindLaunchable("codex", { conns: { codex: { connected: false } } }, live(codex))).toBe(false);
    expect(kindLaunchable("codex", { conns: { codex: { connected: true } } }, live(codex))).toBe(true);
    // A login the policy forbids leaves the kind listed (another of its routes is allowed) but
    // with nothing usable: not offered.
    expect(kindLaunchable("codex", { conns: { codex: { connected: true } } }, live({ ...codex, usableRoutes: [] }))).toBe(false);
  });

  it("offers opencode on a marked Bedrock profile alone, unless its binary is unsupported", () => {
    const oc: LiveKindAccess = { kind: "opencode", routes: ["bedrock"], fixedRoutes: ["opencode-zen"], usableRoutes: ["bedrock"] };
    expect(kindLaunchable("opencode", { conns: { opencode: { connected: false } } }, live(oc))).toBe(true);
    expect(kindLaunchable("opencode", { conns: { opencode: { connected: false, supported: false } } }, live(oc))).toBe(false);
    // Nothing usable at all: not offered, as before.
    expect(kindLaunchable("opencode", { conns: { opencode: { connected: false } } }, live({ ...oc, usableRoutes: [] }))).toBe(false);
    // Another kind with no selectable route keeps its own connection check.
    const codex: LiveKindAccess = { kind: "codex", fixedRoutes: ["openai-api"], usableRoutes: ["openai-api"] };
    expect(kindLaunchable("codex", { conns: { codex: { connected: false } } }, live(codex))).toBe(false);
  });

  it("does not offer a route-only kind to a caller that cannot carry a route (a session move)", () => {
    const oc: LiveKindAccess = { kind: "opencode", routes: ["bedrock"], fixedRoutes: ["opencode-zen"], usableRoutes: ["bedrock"] };
    const conns = { opencode: { connected: false } };
    expect(kindLaunchable("opencode", { conns }, live(oc), { routeless: true })).toBe(false);
    expect(kindLaunchable("opencode", { conns: { opencode: { connected: true } } }, live({ ...oc, usableRoutes: ["bedrock", "opencode-zen"] }), { routeless: true })).toBe(true);
  });

  it("does not offer a kind to a routeless caller when only a selectable route is usable, connected or not", () => {
    // opencode's Free choice leaves it connected, but the policy leaves bedrock as the only usable route.
    const oc: LiveKindAccess = { kind: "opencode", routes: ["bedrock"], fixedRoutes: ["opencode-zen"], usableRoutes: ["bedrock"] };
    expect(kindLaunchable("opencode", { conns: { opencode: { connected: true } } }, live(oc), { routeless: true })).toBe(false);
    expect(kindLaunchable("opencode", { conns: { opencode: { connected: true } } }, live(oc))).toBe(true);
    // claude whose only usable route is Bedrock has no login to launch with.
    const cl = claude(["anthropic-subscription", "bedrock"], ["bedrock"]);
    expect(kindLaunchable("claude", { conns: { claude: { connected: true } } }, live(cl), { routeless: true })).toBe(false);
    // The catalogue alone (connections not read yet) says the same.
    expect(routelessOk("opencode", live(oc))).toBe(false);
    expect(routelessOk("claude", live(cl))).toBe(false);
    expect(routelessOk("claude", live(claude(["anthropic-subscription"], ["anthropic-subscription"])))).toBe(true);
    expect(routelessOk("opencode", null)).toBe(false);
  });

  it("falls back to the connection check while the kind cannot tell its usable routes", () => {
    expect(kindLaunchable("claude", { conns: { claude: { connected: true } } }, live(claude(["anthropic-subscription"])))).toBe(true);
    expect(kindLaunchable("claude", { conns: { claude: { connected: false } } }, live(claude(["anthropic-subscription"])))).toBe(false);
  });
});

describe("emptyRouteOk", () => {
  it("lets claude launch with no route only on an allowed, signed-in subscription", () => {
    expect(emptyRouteOk("claude", claude(["anthropic-api", "anthropic-subscription"], ["anthropic-subscription"]))).toBe(true);
    expect(emptyRouteOk("claude", claude(["anthropic-api", "anthropic-subscription"], ["anthropic-api"]))).toBe(false);
    expect(emptyRouteOk("claude", claude(["anthropic-api"], ["anthropic-api"]))).toBe(false);
  });

  it("leaves every other kind to its own resolution", () => {
    expect(emptyRouteOk("opencode", { kind: "opencode", routes: ["bedrock"], usableRoutes: [] })).toBe(true);
    // opencode with another credential held may still launch with no route.
    expect(emptyRouteOk("opencode", { kind: "opencode", routes: ["bedrock"], fixedRoutes: ["opencode-zen"], usableRoutes: ["bedrock", "opencode-zen"] })).toBe(true);
  });

  it("does not let opencode launch with no route when only a selectable route is usable", () => {
    const oc: LiveKindAccess = { kind: "opencode", routes: ["bedrock"], fixedRoutes: ["opencode-zen"], usableRoutes: ["bedrock"] };
    expect(emptyRouteOk("opencode", oc)).toBe(false);
    expect(quickLaunchRoute("opencode", live(oc), undefined)).toBe("dialog");
  });
});

describe("defaultRouteUsable", () => {
  it("needs the route allowed, its credential held, and its connection listed when known", () => {
    const k = claude(["anthropic-api", "bedrock"], ["bedrock"]);
    expect(defaultRouteUsable(k, { route: "anthropic-api" })).toBe(false);
    expect(defaultRouteUsable(k, { route: "anthropic-subscription" })).toBe(false);
    expect(defaultRouteUsable(k, { route: "bedrock", connection: "p@eu-west-1" }, null)).toBe(true);
    expect(defaultRouteUsable(k, { route: "bedrock", connection: "p@eu-west-1" }, ["q@us-east-1"])).toBe(false);
    expect(defaultRouteUsable(k, { route: "bedrock", connection: "p@eu-west-1" }, ["p@eu-west-1"])).toBe(true);
  });
});

describe("quickLaunchRoute", () => {
  const l = live(claude(["anthropic-api", "anthropic-subscription", "bedrock"], ["anthropic-api", "bedrock"]), {
    kind: "codex",
    fixedRoutes: ["chatgpt-subscription", "openai-api"],
    usableRoutes: ["openai-api"],
  });

  it("sends a usable selectable default", () => {
    expect(quickLaunchRoute("claude", l, { route: "anthropic-api" })).toEqual({ route: { route: "anthropic-api" } });
  });

  it("opens the dialog for a default that cannot be used, never another route", () => {
    expect(quickLaunchRoute("claude", l, { route: "anthropic-subscription" })).toBe("dialog");
    expect(quickLaunchRoute("codex", l, { route: "chatgpt-subscription" })).toBe("dialog");
  });

  it("sends nothing for a usable fixed default: the Agent holds the launch to it", () => {
    expect(quickLaunchRoute("codex", l, { route: "openai-api" })).toEqual({});
  });

  it("opens the dialog for claude on Bedrock, which needs a Bedrock model id", () => {
    expect(quickLaunchRoute("claude", l, { route: "bedrock", connection: "p@" })).toBe("dialog");
  });

  it("opens the dialog when claude has no default and no subscription to run on", () => {
    expect(quickLaunchRoute("claude", l, undefined)).toBe("dialog");
    const signedIn = live(claude(["anthropic-subscription"], ["anthropic-subscription"]));
    expect(quickLaunchRoute("claude", signedIn, undefined)).toEqual({});
  });

  it("opens the dialog with no catalogue", () => {
    expect(quickLaunchRoute("claude", null, undefined)).toBe("dialog");
  });
});

describe("assistant backends", () => {
  it("offers a backend only when the catalogue says its chat is offered", () => {
    const l = live({ kind: "claude", chat: true }, { kind: "codex", chat: false }, { kind: "cursor" });
    expect(chatOffered("claude", l)).toBe(true);
    expect(chatOffered("codex", l)).toBe(false);
    expect(chatOffered("cursor", l)).toBe(false);
    expect(chatOffered("claude", null)).toBe(false);
  });

  it("reorders the visible backends and leaves hidden ones where they were", () => {
    expect(mergeVisibleOrder(["claude", "codex", "cursor", "copilot"], ["copilot", "claude"])).toEqual([
      "copilot",
      "codex",
      "cursor",
      "claude",
    ]);
  });
});
