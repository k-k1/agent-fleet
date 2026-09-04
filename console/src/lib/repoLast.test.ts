import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
  // forgetHiddenRepoModels walks every af.repo-model.* key, so the enumeration API is needed too.
  get length() {
    return values.size;
  },
  key: (i: number) => [...values.keys()][i] ?? null,
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

  // Adding a model to "models to exclude" must also drop it from the per-repo last-used value. If
  // it survives here, that repo's launch path keeps defaulting to the excluded model and the Agent
  // refuses every launch.
  it("forgets remembered models that were just excluded", () => {
    const { forgetHiddenRepoModels, resolveModel, writeRepoLast } = repoLast;
    writeRepoLast("repo-a", "claude", "fable");
    writeRepoLast("repo-b", "claude", "sonnet");
    writeRepoLast("repo-a", "codex", "gpt-5.6-terra");

    forgetHiddenRepoModels("claude", ["fable"]);

    expect(resolveModel("claude", "repo-a", "sonnet")).toBe("sonnet"); // forgotten, back to the default
    expect(resolveModel("claude", "repo-b", "opus")).toBe("sonnet"); // an unrelated value survives
    // claude's key carries no kind, so sweeping it must not take out the other kinds.
    expect(resolveModel("codex", "repo-a", "")).toBe("gpt-5.6-terra");
  });

  // When the exclusion was made on another device, this device's localStorage is still pre-sweep.
  // resolveModel must not adopt an excluded value either, or the Agent's guard refuses every launch.
  it("never resolves to a model excluded in settings", async () => {
    const { resolveModel, writeRepoLast } = repoLast;
    const { setSettings } = await import("./settings.ts");
    setSettings({ hiddenModels: { claude: ["fable"], codex: ["gpt-5.6-terra"] } });
    try {
      writeRepoLast("repo-x", "claude", "fable");
      writeRepoLast("repo-x", "codex", "gpt-5.6-terra");
      expect(resolveModel("claude", "repo-x", "sonnet")).toBe("sonnet"); // drop the memory, use the default
      expect(resolveModel("claude", "repo-y", "fable")).not.toBe("fable"); // the default itself is excluded
      expect(resolveModel("codex", "repo-x", "")).toBe(""); // dynamic kinds leave it to the CLI
    } finally {
      setSettings({ hiddenModels: {} });
    }
  });
});
