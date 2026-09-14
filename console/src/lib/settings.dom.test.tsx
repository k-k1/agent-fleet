import { describe, expect, it } from "vitest";
import { collapseImageProviderOrder, expandImageProviderOrder, expandThinking, getSettings, type ImageFleetRow, IMAGE_PROVIDER_FLEET_GROUP, imageProviderLabel, isDeviceLocalSetting, migrateAiAssistPrefs, normalizeAgentLaunchDefaults, normalizeClaudeCustomModels, normalizeImageProviderOrder, type Settings } from "./settings.ts";

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

// The order the image providers are tried in (ADR 0069). The rule matters because a list saved
// before a provider existed must still RANK it — and WHERE it ranks it decides whose money an
// unattended call spends.
describe("normalizeImageProviderOrder", () => {
  // 🔴 The measured case (ADR 0072, 2026-09-11) was this exact stored value, written before
  // `comfy` existed: `auto` reached the fleet's own GPU only after two personal plans. ADR 0083
  // retired the `sdcpp` id, so the same literal value now exercises the migration decision 7
  // describes: an id the vocabulary no longer knows drops out entirely, and BOTH fleet providers
  // — unmentioned, same as `comfy` was in 2026-09-11 — go to the front together.
  it("drops a retired id and puts every fleet provider the stored list predates at the FRONT", () => {
    expect(normalizeImageProviderOrder(["sdcpp", "agy", "codex"])).toEqual(["openai-compat", "comfy", "agy", "codex"]);
  });

  it("keeps an external provider the stored list predates at the back", () => {
    expect(normalizeImageProviderOrder(["openai-compat", "comfy", "agy"])).toEqual(["openai-compat", "comfy", "agy", "codex"]);
  });

  it("leaves a provider the stored list names where the user put it", () => {
    // Including a fleet one ranked last on purpose: this step only places what was never named.
    expect(normalizeImageProviderOrder(["agy", "codex", "comfy", "openai-compat"])).toEqual(["agy", "codex", "comfy", "openai-compat"]);
  });

  it("honours an explicit reorder and drops unknown ids and duplicates", () => {
    expect(normalizeImageProviderOrder(["codex", "bedrock", "codex", "agy"])).toEqual(["openai-compat", "comfy", "codex", "agy"]);
  });

  it("falls back to the built-in order for a broken stored value", () => {
    expect(normalizeImageProviderOrder("agy")).toEqual(["openai-compat", "comfy", "agy", "codex"]);
  });

  // ADR 0082 P0: the Agent's own provider ids are now per-ROW engine-table keys ("comfy-lan"),
  // not just the four kind names this list still knows. Reading the list that way is P1 (decision
  // 3's second half); this pins the P0 promise instead — a stored value carrying one of the new
  // ids must not throw and must not corrupt the ids this list DOES know, the same graceful drop
  // an already-retired id like `sdcpp` gets above.
  it("drops a row-key id this static list does not know, without disturbing the rest", () => {
    expect(normalizeImageProviderOrder(["comfy-lan", "agy", "codex"])).toEqual(["openai-compat", "comfy", "agy", "codex"]);
    expect(() => normalizeImageProviderOrder(["comfy-lan"])).not.toThrow();
  });
});

// ADR 0082 P1 (decision 3's second half): once the Agent has answered, `rows` REPLACES the two
// static placeholders as the fleet half of the universe — this is the live path AgentsTab uses.
// Every fixture is deliberately keyed the way a real deployment's default row is ("image", not
// "comfy") — a fixture spelled "comfy" cannot catch a reader that still matches the id's
// spelling instead of trusting `rows`, because "comfy" would satisfy either implementation.
describe("normalizeImageProviderOrder（行ベース・live rows）", () => {
  const oneRow: ImageFleetRow[] = [{ id: "image", kind: "comfy" }];
  const twoRows: ImageFleetRow[] = [
    { id: "image", kind: "comfy" },
    { id: "comfy-lan", kind: "comfy" },
  ];

  it("live rows replace the static placeholders entirely", () => {
    expect(normalizeImageProviderOrder([], oneRow)).toEqual(["image", "agy", "codex"]);
  });

  it("a row the stored order predates still goes to the FRONT (the 2026-09-11 rule, live)", () => {
    expect(normalizeImageProviderOrder(["agy", "codex"], oneRow)).toEqual(["image", "agy", "codex"]);
  });

  it("a legacy kind alias expands to every currently declared row of that kind, in order", () => {
    expect(normalizeImageProviderOrder(["comfy", "agy", "codex"], twoRows)).toEqual(["image", "comfy-lan", "agy", "codex"]);
  });

  it("what the member explicitly ranked is still honoured through the alias", () => {
    // The member deliberately put a vendor route ahead of the (then single) fleet engine;
    // expanding the alias must not silently re-promote the row over that choice.
    expect(normalizeImageProviderOrder(["agy", "comfy", "codex"], oneRow)).toEqual(["agy", "image", "codex"]);
  });

  it("a row's own key, once already stored, is not re-expanded a second time", () => {
    expect(normalizeImageProviderOrder(["comfy-lan", "image", "agy"], twoRows)).toEqual(["comfy-lan", "image", "agy", "codex"]);
  });

  it("an empty rows answer means zero fleet rows, NOT a fall back to the two placeholders", () => {
    expect(normalizeImageProviderOrder(["agy", "codex"], [])).toEqual(["agy", "codex"]);
    expect(normalizeImageProviderOrder(["comfy", "agy", "codex"], [])).toEqual(["agy", "codex"]);
  });

  it("omitting rows (undefined) is what falls back — not an empty array", () => {
    expect(normalizeImageProviderOrder(["agy", "codex"])).toEqual(["openai-compat", "comfy", "agy", "codex"]);
  });

  it("drops unknown ids and duplicates, live rows included", () => {
    expect(normalizeImageProviderOrder(["image", "bedrock", "image", "agy"], oneRow)).toEqual(["image", "agy", "codex"]);
  });
});

// What the member SEES is one row for the fleet's own engine, not two rows with the same label
// (ADR 0082). The stored value still carries both ids, so the round trip is what these pin.
describe("the fleet's own engines as one row", () => {
  it("draws one row for them, at the rank the first of them holds", () => {
    expect(collapseImageProviderOrder(["openai-compat", "comfy", "agy", "codex"])).toEqual([
      IMAGE_PROVIDER_FLEET_GROUP,
      "agy",
      "codex",
    ]);
    expect(collapseImageProviderOrder(["agy", "codex", "comfy", "openai-compat"])).toEqual([
      "agy",
      "codex",
      IMAGE_PROVIDER_FLEET_GROUP,
    ]);
  });

  it("labels that row with the service, and the others with their agent name", () => {
    expect(imageProviderLabel(IMAGE_PROVIDER_FLEET_GROUP)).toBe("Agent Fleet (self-hosted)");
    expect(imageProviderLabel("agy")).toBe("");
  });

  it("writes back every id the setting has to carry", () => {
    // A member who drags the group to the bottom must not drop `comfy` from the stored order:
    // it would then be placed at the FRONT again the next time it is normalised.
    expect(expandImageProviderOrder(["agy", "codex", IMAGE_PROVIDER_FLEET_GROUP])).toEqual([
      "agy",
      "codex",
      "openai-compat",
      "comfy",
    ]);
  });

  it("brings ids that were stored apart back together, which is what one row promised", () => {
    const stored = normalizeImageProviderOrder(["openai-compat", "agy", "comfy"]);
    expect(stored).toEqual(["openai-compat", "agy", "comfy", "codex"]);
    expect(expandImageProviderOrder(collapseImageProviderOrder(stored))).toEqual([
      "openai-compat",
      "comfy",
      "agy",
      "codex",
    ]);
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
