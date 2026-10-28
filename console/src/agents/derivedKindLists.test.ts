// The kind lists that used to be typed out by hand are derived from the registry (ADR 0124
// Phase 3). Each one is pinned here to the literal it replaced, as an ordered list where a user
// sees the order, so the derivation is shown equal to what shipped before it.
import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";
import { AGENTS, MEMBER_DEFAULT_AGENTS, inListOrder, repoLaunchKinds, skillsCatalogKinds } from "./registry.ts";
import { ASSISTANT_AGENT_KINDS } from "../lib/settings/options.ts";
import { MCP_KINDS, mcpKinds } from "../features/settings/mcp/mcpWire.ts";
import { copyTargetKinds, type ProjectKindInfo } from "../features/repos/projectMcpWire.ts";
import { SESSION_KINDS } from "../types/session.ts";

// projectMcpWire.ts imports the API client, which reads localStorage at load.
vi.mock("../core/api/client.ts", () => ({ api: vi.fn(), apiJSON: vi.fn(), getTenant: () => "" }));

describe("derived kind lists equal the literals they replaced", () => {
  it("repoLaunchKinds", () => {
    expect(repoLaunchKinds).toEqual(["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "lcpp", "muse", "shell"]);
  });

  it("ASSISTANT_AGENT_KINDS, the default backend priority", () => {
    expect(ASSISTANT_AGENT_KINDS).toEqual(["claude", "codex", "opencode", "cursor", "agy", "muse"]);
  });

  it("MCP_KINDS, and mcpKinds() before a catalog arrives", () => {
    const old = ["claude", "codex", "opencode", "cursor", "kiro", "copilot", "agy", "muse", "lcpp"];
    expect(MCP_KINDS).toEqual(old);
    expect(mcpKinds()).toEqual(old);
  });

  it("MEMBER_DEFAULT_AGENTS", () => {
    expect(MEMBER_DEFAULT_AGENTS).toEqual(["claude", "codex", "opencode", "cursor", "copilot", "kiro", "muse"]);
  });

  it("the Skills tab's kinds", () => {
    expect(skillsCatalogKinds).toEqual(["claude", "codex", "opencode", "muse"]);
  });

  it("the model pickers' live-list and concrete-model kinds (agentModels.ts)", () => {
    const has = (cap: "liveModels" | "concreteModel") => SESSION_KINDS.filter((k) => AGENTS[k].caps[cap]).sort();
    expect(has("liveModels")).toEqual(["agy", "codex", "copilot", "cursor", "kiro", "lcpp", "muse", "opencode"]);
    expect(has("concreteModel")).toEqual(["lcpp"]);
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

describe("inListOrder", () => {
  it("puts ranked kinds first and keeps the input order of the rest", () => {
    expect(inListOrder(["shell", "agy", "zeta", "claude", "opencode"], "assistant")).toEqual(["claude", "opencode", "agy", "shell", "zeta"]);
  });

  it("ranks a kind only in a list it is in, and every rank is unique per list", () => {
    for (const list of ["assistant", "mcp", "memberDefault"] as const) {
      const ranks = SESSION_KINDS.map((k) => AGENTS[k].listOrder?.[list]).filter((r) => r !== undefined);
      expect(new Set(ranks).size, list).toBe(ranks.length);
    }
    for (const k of SESSION_KINDS) {
      const o = AGENTS[k].listOrder ?? {};
      expect(o.assistant !== undefined, `${k} assistant rank`).toBe(AGENTS[k].caps.headlessChat);
      expect(o.memberDefault !== undefined, `${k} memberDefault rank`).toBe(AGENTS[k].caps.memberDefault);
    }
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
