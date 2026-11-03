// The kind lists that used to be typed out by hand are derived from the registry (ADR 0124
// Phase 3). Each one is pinned here as an ordered list where a user sees the order: the literal
// it replaced, except MCP_KINDS and MEMBER_DEFAULT_AGENTS, which moved to SESSION_KINDS order.
import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";
import { AGENTS, MEMBER_DEFAULT_AGENTS, repoLaunchKinds, skillsCatalogKinds } from "./registry.ts";
import { ASSISTANT_AGENT_KINDS } from "../lib/settings/options.ts";
import { MCP_KINDS, mcpKinds } from "../features/settings/mcp/mcpWire.ts";
import { copyTargetKinds, type ProjectKindInfo } from "../features/repos/projectMcpWire.ts";
import { SESSION_KINDS, type SessionKind } from "../types/session.ts";

// projectMcpWire.ts imports the API client, which reads localStorage at load.
vi.mock("../core/api/client.ts", () => ({ api: vi.fn(), apiJSON: vi.fn(), getTenant: () => "" }));

// The kinds that existed when the literals below were pinned. Each derived list is compared with
// its literal only over these kinds; a kind added later is held by its declaration (the derivation
// rule), so it needs no edit here. The set itself is pinned so it cannot shrink unnoticed.
const FROZEN_KINDS: readonly string[] = [
  "claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "lcpp", "muse", "shell", "ssm",
];
const onFrozen = (ks: readonly string[]) => ks.filter((k) => FROZEN_KINDS.includes(k));

it("the frozen kind set is not shrunk", () => {
  expect([...FROZEN_KINDS].sort()).toEqual(
    ["agy", "claude", "codex", "copilot", "cursor", "kiro", "lcpp", "muse", "opencode", "shell", "ssm"],
  );
  for (const k of FROZEN_KINDS) expect(SESSION_KINDS as readonly string[], k).toContain(k);
});

describe("derived kind lists equal the literals they replaced", () => {
  it("repoLaunchKinds", () => {
    // One declaration per existing kind (launchableFromRepo), instead of a second copy of the
    // list: ssm is the only kind a repo row cannot start. A kind added later is unconstrained
    // here and lands in the menu wherever SESSION_KINDS puts it.
    const launchable: Record<string, boolean> = {
      claude: true, codex: true, cursor: true, copilot: true, kiro: true,
      agy: true, opencode: true, lcpp: true, muse: true, shell: true, ssm: false,
    };
    for (const [kind, want] of Object.entries(launchable)) {
      expect(repoLaunchKinds.includes(kind as SessionKind), kind).toBe(want);
      expect(AGENTS[kind as SessionKind].caps.launchableFromRepo, kind).toBe(want);
    }
    expect(repoLaunchKinds).toEqual(SESSION_KINDS.filter((k) => AGENTS[k].caps.launchableFromRepo));
  });

  it("ASSISTANT_AGENT_KINDS, the default backend priority", () => {
    expect(onFrozen(ASSISTANT_AGENT_KINDS)).toEqual(["claude", "codex", "opencode", "cursor", "agy", "muse"]);
    // Any kind: exactly the headless-chat kinds are offered.
    expect([...ASSISTANT_AGENT_KINDS].sort()).toEqual(SESSION_KINDS.filter((k) => AGENTS[k].caps.headlessChat).sort());
  });

  it("MCP_KINDS, and mcpKinds() before a catalog arrives, in SESSION_KINDS order", () => {
    // was: claude codex opencode cursor kiro copilot agy muse lcpp
    const want = ["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "lcpp", "muse"];
    expect(onFrozen(MCP_KINDS)).toEqual(want);
    expect(onFrozen(mcpKinds())).toEqual(want);
    // Any kind: SESSION_KINDS order, and the fallback and the pre-catalog list agree.
    expect(mcpKinds().filter((k) => (SESSION_KINDS as readonly string[]).includes(k))).toEqual([...MCP_KINDS]);
  });

  it("MEMBER_DEFAULT_AGENTS, in SESSION_KINDS order", () => {
    // was: claude codex opencode cursor copilot kiro muse
    expect(onFrozen(MEMBER_DEFAULT_AGENTS)).toEqual(["claude", "codex", "cursor", "copilot", "kiro", "opencode", "muse"]);
    expect(MEMBER_DEFAULT_AGENTS).toEqual(SESSION_KINDS.filter((k) => AGENTS[k].caps.memberDefault));
  });

  it("the Skills tab's kinds", () => {
    expect(onFrozen(skillsCatalogKinds)).toEqual(["claude", "codex", "opencode", "muse"]);
    expect(skillsCatalogKinds).toEqual(SESSION_KINDS.filter((k) => AGENTS[k].caps.skillsCatalog));
  });

  it("the model pickers' live-list and concrete-model kinds (agentModels.ts)", () => {
    const has = (cap: "liveModels" | "concreteModel") => SESSION_KINDS.filter((k) => AGENTS[k].caps[cap]).sort();
    expect(onFrozen(has("liveModels"))).toEqual(["agy", "codex", "copilot", "cursor", "kiro", "lcpp", "muse", "opencode"]);
    expect(onFrozen(has("concreteModel"))).toEqual(["lcpp"]);
  });

  it("the project copy panel's targets, from the Agent's per-kind table", () => {
    // workspace/agent/internal/mcpproj/inspect.go kindInfos, in its order, as the snapshot sends it.
    const kinds: ProjectKindInfo[] = [
      { kind: "claude", hasProjectScope: true },
      { kind: "codex", hasProjectScope: true },
      { kind: "opencode", hasProjectScope: true },
      { kind: "cursor", hasProjectScope: true },
      { kind: "copilot", hasProjectScope: true },
      { kind: "kiro", hasProjectScope: true, unverified: true },
      { kind: "agy", hasProjectScope: false },
      { kind: "muse", hasProjectScope: false },
    ];
    expect(copyTargetKinds({ kinds })).toEqual(["claude", "codex", "opencode", "cursor", "copilot"]);
  });
});

describe("assistantRank", () => {
  it("ranks exactly the headless-chat kinds, each rank once", () => {
    const ranks = SESSION_KINDS.map((k) => AGENTS[k].assistantRank).filter((r) => r !== undefined);
    expect(new Set(ranks).size).toBe(ranks.length);
    for (const k of SESSION_KINDS) expect(AGENTS[k].assistantRank !== undefined, k).toBe(AGENTS[k].caps.headlessChat);
  });
});

// The CP validates a tenant's default agent against its own list. An agent only the Console
// offers is refused on save; one only the CP accepts can never be chosen. A checkout without the
// Go file skips rather than fails.
const CP_FILE = new URL("../../../control-plane/tenant_member_defaults.go", import.meta.url);

describe.skipIf(!existsSync(CP_FILE))("MEMBER_DEFAULT_AGENTS against the Control Plane", () => {
  it("equals memberDefaultAgents in tenant_member_defaults.go, in order", () => {
    const m = /var memberDefaultAgents = \[\]string\{([^}]*)\}/.exec(readFileSync(CP_FILE, "utf8"));
    expect(m, "memberDefaultAgents not found in tenant_member_defaults.go (renamed?)").not.toBeNull();
    const cp = [...m![1].matchAll(/"([a-z]+)"/g)].map((q) => q[1]);
    expect(cp.length).toBeGreaterThan(0);
    expect([...MEMBER_DEFAULT_AGENTS]).toEqual(cp);
  });
});
