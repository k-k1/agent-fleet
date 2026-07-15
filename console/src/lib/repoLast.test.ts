import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("window", { fetch: vi.fn(async () => new Response()) });

let repoLast: typeof import("./repoLast.ts");

beforeAll(async () => {
  // repoLast imports settings, whose API client reads localStorage at module load.
  repoLast = await import("./repoLast.ts");
});

describe("repo launch settings", () => {
  beforeEach(() => values.clear());

  it("falls back to each agent's global defaults", () => {
    const { resolveEffort, resolveModel, resolveStartMode } = repoLast;
    expect(resolveModel("codex", "repo", "gpt-default")).toBe("gpt-default");
    expect(resolveEffort("claude", "repo", "high")).toBe("high");
    expect(resolveStartMode("opencode", "repo", "plan")).toBe("plan");
  });

  it("remembers explicit standard values above non-empty defaults", () => {
    const { readRepoLast, resolveEffort, resolveModel, resolveStartMode, writeRepoLast } = repoLast;
    writeRepoLast("repo", "codex", "", "", "normal");
    expect(readRepoLast("repo").kind).toBe("codex");
    expect(resolveModel("codex", "repo", "gpt-default")).toBe("");
    expect(resolveEffort("codex", "repo", "high")).toBe("");
    expect(resolveStartMode("codex", "repo", "plan")).toBe("normal");
  });

  it("keeps values isolated by agent kind", () => {
    const { resolveEffort, resolveModel, resolveStartMode, writeRepoLast } = repoLast;
    writeRepoLast("repo", "claude", "opus", "max", "plan");
    writeRepoLast("repo", "opencode", "openai/gpt", "high", "normal");
    expect(resolveModel("claude", "repo", "sonnet")).toBe("opus");
    expect(resolveEffort("claude", "repo", "low")).toBe("max");
    expect(resolveStartMode("claude", "repo", "normal")).toBe("plan");
    expect(resolveModel("opencode", "repo", "")).toBe("openai/gpt");
  });
});
