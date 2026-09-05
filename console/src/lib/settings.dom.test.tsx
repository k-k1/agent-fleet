import { describe, expect, it } from "vitest";
import { expandThinking, getSettings, isDeviceLocalSetting, migrateAiAssistPrefs, normalizeAgentLaunchDefaults, normalizeClaudeCustomModels, type Settings } from "./settings.ts";

// Pure logic, but it lives in the jsdom project (.dom.test.tsx): settings.ts touches
// localStorage at load time through the API client, so under node the import itself fails.
//
// "Expand thinking" (Settings > Agents > each card > Behaviour) is kind-scoped, and an
// unset value must always mean off, i.e. collapsed as before. Server-side ui-prefs are also
// written by other Console versions, so this pins that a non-boolean value falls to off too.
const withThinking = (map: unknown): Settings =>
  ({ ...getSettings(), expandThinking: map } as Settings);

describe("expandThinking", () => {
  it("defaults to off for every kind", () => {
    const s = withThinking({});
    expect(expandThinking(s, "opencode")).toBe(false);
    expect(expandThinking(s, "codex")).toBe(false);
    expect(expandThinking(s, "claude")).toBe(false);
  });

  it("applies per kind, independently", () => {
    const s = withThinking({ opencode: true, codex: false });
    expect(expandThinking(s, "opencode")).toBe(true);
    expect(expandThinking(s, "codex")).toBe(false);
    // A kind with no entry (cursor / kiro also emit thinking) must not be dragged along.
    expect(expandThinking(s, "cursor")).toBe(false);
  });

  it("falls back to off for a missing kind or a broken stored value", () => {
    const s = withThinking({ opencode: "yes", codex: 1 });
    expect(expandThinking(s, "opencode")).toBe(false);
    expect(expandThinking(s, "codex")).toBe(false);
    expect(expandThinking(s, undefined)).toBe(false);
    expect(expandThinking(withThinking(undefined), "opencode")).toBe(false);
  });
});

describe("normalizeClaudeCustomModels", () => {
  it("trims ids and drops aliases, duplicates, and broken values", () => {
    expect(normalizeClaudeCustomModels([
      " claude-opus-4-8 ", "CLAUDE-OPUS-4-8", "claude-opus-4-7", "claude-opus-4-6[1m]", "opus", "bad model", 42, "",
    ])).toEqual(["claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6[1m]"]);
  });

  it("falls back to an empty catalog for a broken stored value", () => {
    expect(normalizeClaudeCustomModels("claude-opus-4-8")).toEqual([]);
  });
});

describe("device-local settings", () => {
  it("keeps the pane layout profile on this device", () => {
    expect(isDeviceLocalSetting("paneLayout")).toBe(true);
  });

  it("continues syncing personal content preferences", () => {
    expect(isDeviceLocalSetting("viewerFont")).toBe(false);
    expect(isDeviceLocalSetting("locale")).toBe(false);
  });

  // A per-region theme/background is "how this device should look", so it stays device-local.
  // Shared sessions (docs/log/59) are treated the same — leaving a new key out of this list
  // silently overwrites how another device renders.
  it("keeps every per-region look on this device", () => {
    for (const key of ["mirrorTheme", "sharedTheme", "assistantTheme", "chatColor", "sharedColor"] as const) {
      expect(isDeviceLocalSetting(key)).toBe(true);
    }
  });
});

// The permission-prompt default (docs/log/76). An absent key must mean "skip": a device that
// reads existing prefs and suddenly parks every session on an approval prompt is the least
// visible way to break "the default stays as it is". Approvals only when false is explicit.
describe("normalizeAgentLaunchDefaults / skipPermissions", () => {
  it("defaults to skipping approvals when the key is absent or broken", () => {
    const rows = normalizeAgentLaunchDefaults({ claude: { model: "opus" }, cursor: { skipPermissions: "no" } });
    expect(rows.claude.skipPermissions).toBe(true);
    expect(rows.cursor.skipPermissions).toBe(true);
    // Same for a kind that was never configured at all (the DEFAULT_AGENT_LAUNCH row).
    expect(rows.kiro.skipPermissions).toBe(true);
  });

  it("keeps an explicit opt-in to approvals, per kind", () => {
    const rows = normalizeAgentLaunchDefaults({ claude: { skipPermissions: false }, kiro: { skipPermissions: true } });
    expect(rows.claude.skipPermissions).toBe(false);
    expect(rows.kiro.skipPermissions).toBe(true);
    // Does not leak into other kinds.
    expect(rows.cursor.skipPermissions).toBe(true);
  });
});

// --- Migration for the AI-assist split (docs/log/84) ---------------------------
//
// The rule is "an upgrade never changes behaviour". aiProseModels is the deliberate
// exception and is not carried over: despite its name, the old key also replaced the prose
// generation default. That load() (localStorage) and hydrateUIPrefs() (server prefs) go
// through the same function is pinned here rather than in settingsSync — the original
// failure was the rule being copied into two places.
describe("migrateAiAssistPrefs", () => {
  it("splits the old title toggle into session / chat / branch", () => {
    const o: Record<string, unknown> = { autoTitleSuggest: false };
    migrateAiAssistPrefs(o);
    expect(o.assistantTitleSuggest).toBe(false);
    expect(o.branchSuggestEnabled).toBe(false);
  });

  it("never overwrites a key the user already set", () => {
    const o: Record<string, unknown> = { autoTitleSuggest: false, branchSuggestEnabled: true };
    migrateAiAssistPrefs(o);
    expect(o.branchSuggestEnabled).toBe(true);
  });

  it("carries the single agent order over to the assist side", () => {
    const o: Record<string, unknown> = { assistantAgentOrder: ["codex", "claude"] };
    migrateAiAssistPrefs(o);
    expect((o.aiAssistOrder as string[])[0]).toBe("codex");
    // The chat side is left as it was (the split never moves only one of them).
    expect((o.assistantAgentOrder as string[])[0]).toBe("codex");
  });

  it("inherits the legacy utility models into BOTH tiers", () => {
    // Despite its name the old key drove both the short-text and the prose tier. Moving
    // just one of them to a different model at the split would change behaviour for a user
    // who touched nothing.
    const o: Record<string, unknown> = { assistantUtilityModels: { claude: "haiku" } };
    migrateAiAssistPrefs(o);
    expect(o.aiShortModels).toEqual({ claude: "haiku" });
    expect(o.aiProseModels).toEqual({ claude: "haiku" });
  });

  it("does not touch a tier the user has already split", () => {
    const o: Record<string, unknown> = {
      assistantUtilityModels: { claude: "haiku" },
      aiProseModels: { claude: "sonnet" },
    };
    migrateAiAssistPrefs(o);
    expect(o.aiProseModels).toEqual({ claude: "sonnet" });
  });

  it("gives read-aloud its own language key, seeded from the borrowed one", () => {
    const o: Record<string, unknown> = { outputLanguage: "en" };
    migrateAiAssistPrefs(o);
    expect(o.ttsLang).toBe("en");
  });

  it("leaves a pre-split blob alone when nothing legacy is present", () => {
    const o: Record<string, unknown> = {};
    migrateAiAssistPrefs(o);
    expect(o).toEqual({});
  });
});
