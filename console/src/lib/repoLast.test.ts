import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
  // forgetHiddenRepoModels は「af.repo-model.* を総なめ」なので列挙 API も要る。
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

  // 「使わないモデル」に入れたら、リポジトリごとの前回値からも消える。ここが残ると
  // そのリポジトリの起動導線だけ除外モデルを既定に持ち続け、毎回 Agent 側で弾かれる。
  it("forgets remembered models that were just excluded", () => {
    const { forgetHiddenRepoModels, resolveModel, writeRepoLast } = repoLast;
    writeRepoLast("repo-a", "claude", "fable");
    writeRepoLast("repo-b", "claude", "sonnet");
    writeRepoLast("repo-a", "codex", "gpt-5.6-terra");

    forgetHiddenRepoModels("claude", ["fable"]);

    expect(resolveModel("claude", "repo-a", "sonnet")).toBe("sonnet"); // 忘れて既定へ
    expect(resolveModel("claude", "repo-b", "opus")).toBe("sonnet"); // 無関係な値は残る
    // claude のキーは kind 無し（歴史的経緯）なので、他 kind を巻き添えにしないこと。
    expect(resolveModel("codex", "repo-a", "")).toBe("gpt-5.6-terra");
  });

  // 別端末で除外された場合、この端末の localStorage は掃除前のまま。resolveModel 側でも
  // 除外値を採用しない（採用すると起動のたびに Agent 側ガードで弾かれる）。
  it("never resolves to a model excluded in settings", async () => {
    const { resolveModel, writeRepoLast } = repoLast;
    const { setSettings } = await import("./settings.ts");
    setSettings({ hiddenModels: { claude: ["fable"], codex: ["gpt-5.6-terra"] } });
    try {
      writeRepoLast("repo-x", "claude", "fable");
      writeRepoLast("repo-x", "codex", "gpt-5.6-terra");
      expect(resolveModel("claude", "repo-x", "sonnet")).toBe("sonnet"); // 記憶を捨てて既定へ
      expect(resolveModel("claude", "repo-y", "fable")).not.toBe("fable"); // 既定自体が除外
      expect(resolveModel("codex", "repo-x", "")).toBe(""); // 動的 kind は CLI 任せへ
    } finally {
      setSettings({ hiddenModels: {} });
    }
  });
});
