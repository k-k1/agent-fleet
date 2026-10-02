// Storage rules of the launch modal's prompt history and personal templates (#1469): both ride on
// the synced ui-prefs, so they follow the user across devices — and so they must not lose the
// device-only history older Consoles kept, must not overwrite other devices' history before the
// server copy has been read, and must stay inside a size budget (one ui-prefs PUT over the
// Agent's 64 KiB limit fails the sync of every setting).
import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

// settings.ts is one global state read at import time, so every case loads the modules afresh.
async function fresh() {
  vi.resetModules();
  const settings = await import("./settings.ts");
  const history = await import("./promptHistory.ts");
  const templates = await import("./launchTemplates.ts");
  return { settings, history, templates };
}

beforeEach(() => {
  vi.useFakeTimers();
  apiMock.mockReset();
  apiJSONMock.mockReset().mockResolvedValue({});
  localStorage.clear();
});

describe("prompt history", () => {
  it("moves the device-only history into the synced list without losing any of it", async () => {
    localStorage.setItem("af.repo-prompts.app", JSON.stringify(["old one", "shared"]));
    localStorage.setItem("af.repo-prompts.app@feature-x", JSON.stringify(["from a worktree"]));
    const { settings, history } = await fresh();

    // Before the server copy is read: shown, but not moved (a write now would beat the server).
    expect(history.readPromptHistory("app")).toEqual(["old one", "shared", "from a worktree"]);
    expect(history.migrateLegacyPromptHistory()).toBe(false);
    expect(localStorage.getItem("af.repo-prompts.app")).not.toBeNull();

    // Another device already synced "from phone" and "shared".
    apiMock.mockResolvedValueOnce({
      launchHistory: [
        { repo: "app", text: "from phone", at: 5 },
        { repo: "app", text: "shared", at: 4 },
      ],
    });
    expect(await settings.hydrateUIPrefs()).toBe(true);
    expect(history.migrateLegacyPromptHistory()).toBe(true);
    expect(history.readPromptHistory("app")).toEqual(["from phone", "shared", "old one", "from a worktree"]);
    expect(localStorage.getItem("af.repo-prompts.app")).toBeNull();
    expect(localStorage.getItem("af.repo-prompts.app@feature-x")).toBeNull();

    await vi.advanceTimersByTimeAsync(1_000);
    const body = apiJSONMock.mock.calls.at(-1)?.[2] as { launchHistory: { text: string }[] };
    expect(body.launchHistory.map((e) => e.text)).toEqual(["from phone", "shared", "old one", "from a worktree"]);
  });

  it("keeps a launch made before the server copy is read on this device until it can be merged", async () => {
    const { settings, history } = await fresh();
    history.pushPromptHistory("app", "early launch");
    expect(settings.getSettings().launchHistory).toEqual([]); // not over the server's list
    expect(JSON.parse(localStorage.getItem("af.repo-prompts.app") || "[]")).toEqual(["early launch"]);

    apiMock.mockResolvedValueOnce({ launchHistory: [{ repo: "app", text: "elsewhere", at: 1 }] });
    await settings.hydrateUIPrefs();
    history.pushPromptHistory("app", "later launch");
    expect(history.readPromptHistory("app")).toEqual(["later launch", "elsewhere", "early launch"]);
    expect(localStorage.getItem("af.repo-prompts.app")).toBeNull();
  });

  it("dedupes, caps per repository, and files a worktree under its base clone", async () => {
    apiMock.mockResolvedValueOnce({});
    const { settings, history } = await fresh();
    await settings.hydrateUIPrefs();
    for (let i = 0; i < 10; i++) history.pushPromptHistory("app", "p" + i);
    history.pushPromptHistory("app@wt", "p5");
    expect(history.readPromptHistory("app")).toEqual(["p5", "p9", "p8", "p7", "p6", "p4", "p3", "p2"]);
    expect(settings.getSettings().launchHistory.every((e) => e.repo === "app")).toBe(true);
  });

  it("keeps a prompt too large for the synced budget on this device only", async () => {
    apiMock.mockResolvedValueOnce({});
    const { settings, history } = await fresh();
    await settings.hydrateUIPrefs();
    const big = "x".repeat(history.HISTORY_ENTRY_MAX_BYTES + 1);
    history.pushPromptHistory("app", big);
    expect(settings.getSettings().launchHistory).toEqual([]);
    expect(history.readPromptHistory("app")).toEqual([big]);
    expect(history.migrateLegacyPromptHistory()).toBe(false); // stays put, still shown
    expect(history.readPromptHistory("app")).toEqual([big]);

    history.deletePromptHistory("app", big);
    expect(history.readPromptHistory("app")).toEqual([]);
  });

  it("stays inside the byte budget, dropping the oldest first", async () => {
    const { history } = await fresh();
    const entries = Array.from({ length: 40 }, (_, i) => ({ repo: "r" + i, text: "y".repeat(1000), at: 40 - i }));
    const kept = history.normalizeHistory(entries);
    expect(new TextEncoder().encode(JSON.stringify(kept)).length).toBeLessThanOrEqual(history.HISTORY_MAX_BYTES);
    expect(kept[0].repo).toBe("r0");
    expect(kept.length).toBeLessThan(40);
    expect(history.normalizeHistory([{ repo: "a" }, "junk", { repo: "a", text: " t ", at: 1 }])).toEqual([{ repo: "a", text: "t", at: 1 }]);
  });
});

describe("personal templates", () => {
  it("validates, scopes to a base repository, updates in place and deletes", async () => {
    const { settings, templates } = await fresh();
    expect(templates.saveTemplate({ name: " ", body: "b", repo: "" })).toEqual({ ok: false, error: "name_required" });
    expect(templates.saveTemplate({ name: "n", body: "  ", repo: "" })).toEqual({ ok: false, error: "body_required" });
    expect(templates.saveTemplate({ name: "n", body: "x".repeat(templates.TEMPLATE_BODY_MAX_BYTES + 1), repo: "" })).toEqual({
      ok: false,
      error: "body_too_long",
    });

    const a = templates.saveTemplate({ name: "All", body: "everywhere", repo: "" });
    const b = templates.saveTemplate({ name: "Mine", body: "app only", repo: "app@feature-x" });
    if (!a.ok || !b.ok) throw new Error("save failed");
    const all = templates.readTemplates();
    expect(all.map((t) => [t.name, t.repo])).toEqual([
      ["All", ""],
      ["Mine", "app"],
    ]);
    expect(templates.templatesFor(all, "app@other").map((t) => t.name)).toEqual(["All", "Mine"]);
    expect(templates.templatesFor(all, "web").map((t) => t.name)).toEqual(["All"]);

    expect(templates.saveTemplate({ id: a.id, name: "Renamed", body: "everywhere", repo: "" })).toEqual({ ok: true, id: a.id });
    expect(templates.readTemplates().map((t) => t.name)).toEqual(["Renamed", "Mine"]);
    templates.deleteTemplate(b.id);
    expect(settings.getSettings().launchTemplates.map((t) => t.name)).toEqual(["Renamed"]);
  });

  it("refuses a save that would outgrow the budget, and trims an oversized list it reads", async () => {
    const { templates } = await fresh();
    const body = "z".repeat(6000);
    expect(templates.saveTemplate({ name: "1", body, repo: "" }).ok).toBe(true);
    expect(templates.saveTemplate({ name: "2", body, repo: "" }).ok).toBe(true);
    expect(templates.saveTemplate({ name: "3", body, repo: "" })).toEqual({ ok: false, error: "full" });
    expect(templates.readTemplates()).toHaveLength(2);

    const many = Array.from({ length: 60 }, (_, i) => ({ id: "t" + i, name: "n" + i, body: "b", repo: "", at: i }));
    const kept = templates.normalizeTemplates([...many, { id: "bad" }, { id: "t1", name: "dup", body: "b", repo: "" }]);
    expect(kept).toHaveLength(templates.TEMPLATE_MAX_COUNT);
    expect(kept[0].id).toBe("t20"); // the oldest edits are what the cap drops; order is kept
  });

  it("is synced and protected like other accumulated data", async () => {
    const { settings } = await fresh();
    expect(settings.isAccumulatedSetting("launchTemplates")).toBe(true);
    expect(settings.isAccumulatedSetting("launchHistory")).toBe(true);
    expect(settings.isDeviceLocalSetting("launchTemplates")).toBe(false);
  });

  it("summarises a prompt by its first line", async () => {
    const { templates } = await fresh();
    expect(templates.promptTitle("\n\n## Fix it\nbody  text\nmore")).toBe("Fix it");
    expect(templates.promptExcerpt("\n## Fix it\nbody  text\nmore")).toBe("body text more");
  });
});
