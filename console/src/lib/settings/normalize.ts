// How a stored preference is read: the per-key normalisers the descriptors in prefs.ts name, the
// legacy migrations load and hydrate run before merging, and the accessors that read a
// kind-scoped map. Each mirrors the Agent's own reading of the same ui-prefs key, so what the
// screen shows is what the Agent enforces. Re-exported from lib/settings.ts.
import { ASSISTANT_AGENT_KINDS, ASSISTANT_RECOMMENDED_MODEL, CLAUDE_MODELS, DEFAULT_MODEL } from "./options.ts";
import type { Settings } from "./prefs.ts";

// Settings for one read-aloud character (a ttsVoicePool value). Every field is optional; an
// absent field means the default behaviour.
export interface TtsCharConf {
  use?: boolean;
  style?: string;
  speed?: number;
}

export interface AgentLaunchDefault {
  model: string;
  effort: string;
  startMode: "normal" | "plan";
  /**
   * Launch with the permission prompts skipped (docs/log/76). true = the long-standing fleet
   * default (--dangerously-skip-permissions for claude, the equivalent flag for other kinds);
   * false = ask for approval on every tool run. The default is true, so behaviour is unchanged.
   *
   * The Agent reads this value in-process too (ui_prefs.go skipPermissionsPref), so the same
   * default applies to launches that do not go through the Console (MCP create_session,
   * scheduled runs, restart, fork). Ignored for kinds without caps.permissionChoice
   * (codex / opencode).
   */
  skipPermissions: boolean;
}

export type AgentLaunchDefaults = Record<string, AgentLaunchDefault>;

// normalizeAssistantOrder folds any stored value into a total order over
// ASSISTANT_AGENT_KINDS: unknown entries and dupes drop, missing kinds append in
// the built-in order — same rules as the Agent's assistantAgentOrderPref, so what
// the UI shows is exactly what the backend will do.
export function normalizeAssistantOrder(v: unknown): string[] {
  const out: string[] = [];
  const push = (k: unknown) => {
    if (typeof k === "string" && (ASSISTANT_AGENT_KINDS as readonly string[]).includes(k) && !out.includes(k)) out.push(k);
  };
  if (Array.isArray(v)) v.forEach(push);
  ASSISTANT_AGENT_KINDS.forEach(push);
  return out;
}

// The per-kind model maps whose missing entries mean "recommended" (fillRecommendedModelMaps).
const RECOMMENDED_MODEL_MAPS = ["assistantModels", "aiShortModels", "aiProseModels"] as const;

// fillRecommendedModelMaps gives every assistant kind an entry in the three per-kind model maps,
// "recommended" where it has none, and says whether it added any. A missing entry was a state of
// its own: the settings row showed it as the CLI default ("" — `map[kind] || ""`) while the Agent
// ran its per-kind historical default or AF_TITLE_MODEL_*, so screen and run disagreed
// (#972 review, round 3). The Agent now reads a missing entry as "recommended" as well
// (ui_prefs.go), so this only has to make the screen say so; it never writes back. It arose wherever a map was shallow-merged over DEFAULTS — a legacy
// assistantUtilityModels copy (migrateAiAssistPrefs) lacking a kind, or a kind added after the
// map was first saved (muse). "recommended" is what DEFAULTS give a new member for every kind,
// so this makes an older prefs file mean what a new one means. Explicit values, "" (the CLI
// default) included, are never touched.
export function fillRecommendedModelMaps(o: Record<string, unknown>): boolean {
  let added = false;
  for (const key of RECOMMENDED_MODEL_MAPS) {
    const cur = o[key];
    if (cur === undefined) continue; // absent: DEFAULTS supply the whole map
    const map: Record<string, unknown> = cur && typeof cur === "object" && !Array.isArray(cur) ? { ...cur } : {};
    let changed = false;
    for (const kind of ASSISTANT_AGENT_KINDS) {
      if (!(kind in map)) {
        map[kind] = ASSISTANT_RECOMMENDED_MODEL;
        changed = true;
      }
    }
    if (changed) {
      o[key] = map;
      added = true;
    }
  }
  return added;
}

// migrateAiAssistPrefs — carry-over for splitting AI assist generation out of the assistant
// (docs/log/84). It only fills in missing keys and never touches a value that is already there.
//
// Both load() (localStorage) and hydrateUIPrefs() (server prefs) call this same function.
// Writing the rule out twice invites fixing only one copy — the old assistantTitleSuggest
// migration really had been transcribed into two places.
//
// The principle is that an upgrade must not change behaviour: BOTH aiShortModels and
// aiProseModels inherit the old assistantUtilityModels value, carrying the pre-split behaviour
// over unchanged (docs/log/84 §84.3's ★ — an earlier version of this migration had prose go
// back to its own recommendation instead, which was reverted because it changed a model out
// from under a user who had not touched this setting the moment the split shipped). The point
// of splitting the keys is that they can move independently AFTER this migration runs, not that
// they start apart (docs/log/103 §103.3-5: this function's own doc comment used to say the
// opposite of what the code below does — fixed here, not just in the code).
export function migrateAiAssistPrefs(o: Record<string, unknown>): void {
  // Title suggestion moves to one key per feature. The old autoTitleSuggest covered sessions,
  // chats and branch names at once, so an explicit OFF is carried over to all three.
  if (typeof o.autoTitleSuggest === "boolean") {
    if (!("assistantTitleSuggest" in o)) o.assistantTitleSuggest = o.autoTitleSuggest;
    if (!("branchSuggestEnabled" in o)) o.branchSuggestEnabled = o.autoTitleSuggest;
  }
  // The CLI priority splits into a chat list and an assist-generation list. It was one list
  // before, so the assist side inherits that order verbatim; the split changes nothing anyone
  // would notice.
  if (!("aiAssistOrder" in o) && Array.isArray(o.assistantAgentOrder)) {
    o.aiAssistOrder = normalizeAssistantOrder(o.assistantAgentOrder);
  }
  // Both the short and the prose keys inherit the old utility models. Despite its name the old
  // key really did drive both, so this is the initial value that carries the pre-release
  // behaviour over unchanged. The point of splitting them is being able to change them
  // independently later, not moving one to a different model the moment this ships.
  if (o.assistantUtilityModels && typeof o.assistantUtilityModels === "object") {
    const legacy = o.assistantUtilityModels as Record<string, string>;
    if (!("aiShortModels" in o)) o.aiShortModels = { ...legacy };
    if (!("aiProseModels" in o)) o.aiProseModels = { ...legacy };
  }
  // The read-aloud language stops borrowing outputLanguage and gets its own key. It inherits the
  // borrowed value, so the current behaviour — reply language English means read-aloud goes to
  // Polly — is preserved. From now on either one can be changed alone.
  if (!("ttsLang" in o) && typeof o.outputLanguage === "string") o.ttsLang = o.outputLanguage;
  // The chat's own ✨ gets its own key (docs/log/103 §103.3-3/§103.9): an explicit legacy OFF
  // on the mirror's replySuggestEnabled carries over once, so nobody who had turned suggestions
  // off finds the chat's ✨ switched back on the moment the two keys separate. A missing or
  // true value needs no migration — the new key's own default is already ON.
  if (!("assistantReplySuggestEnabled" in o) && o.replySuggestEnabled === false) {
    o.assistantReplySuggestEnabled = false;
  }
}

export const DEFAULT_AGENT_LAUNCH: AgentLaunchDefaults = {
  claude: { model: DEFAULT_MODEL, effort: "", startMode: "normal", skipPermissions: true },
  codex: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  cursor: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  copilot: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  kiro: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  agy: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  opencode: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  lcpp: { model: "", effort: "", startMode: "normal", skipPermissions: true },
  // muse's "" model is not "whatever muse picks": the Agent resolves an empty model to the
  // newest NON-contributor entry of the live catalog before it starts the session (ADR 0095
  // decision 6 clamp 8 — the host's own default is the variant whose conversations may be used
  // for product improvement). Pinning an id here instead would go stale at the next release.
  muse: { model: "", effort: "", startMode: "normal", skipPermissions: true },
};

/** AF memory is on only for a boolean true, the Agent's own reading (uiprefs.AgentMemory). */
export function normalizeAgentMemory(value: unknown): boolean {
  return value === true;
}

export type ExternalMemoryMode = "off" | "read" | "write";

/** Anything but "read" or "write" is off, the Agent's own reading (uiprefs.ExternalMemory). */
export function normalizeExternalMemory(value: unknown): ExternalMemoryMode {
  return value === "read" || value === "write" ? value : "off";
}

export type MemoryReviewMode = "off" | "propose";

/** Anything but "propose" is off, the Agent's own reading (uiprefs.MemoryReview). */
export function normalizeMemoryReview(value: unknown): MemoryReviewMode {
  return value === "propose" ? value : "off";
}

export function normalizeClaudeCustomModels(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const out: string[] = [];
  const seen = new Set<string>();
  for (const raw of value) {
    if (typeof raw !== "string") continue;
    const id = raw.trim();
    const key = id.toLowerCase();
    if (!/^claude-[a-z0-9][a-z0-9._\-[\]]*$/i.test(id) || seen.has(key) ||
        CLAUDE_MODELS.some(([alias]) => alias.toLowerCase() === key)) continue;
    seen.add(key);
    out.push(id);
  }
  return out;
}

export function normalizeAgentLaunchDefaults(rows: unknown, legacyClaudeModel = DEFAULT_MODEL): AgentLaunchDefaults {
  const src = rows && typeof rows === "object" ? rows as Record<string, Partial<AgentLaunchDefault>> : {};
  const out: AgentLaunchDefaults = {};
  // Drive the set of normalized kinds from DEFAULT_AGENT_LAUNCH — the single source of
  // truth — rather than a parallel literal list that silently drifts (copilot was
  // dropped that way, discarding its saved launch defaults on reload). Adding a kind's
  // default above now normalizes it automatically.
  for (const kind of Object.keys(DEFAULT_AGENT_LAUNCH)) {
    const base = DEFAULT_AGENT_LAUNCH[kind];
    const row = src[kind] || {};
    out[kind] = {
      model: typeof row.model === "string" ? row.model : kind === "claude" ? legacyClaudeModel : base.model,
      effort: typeof row.effort === "string" ? row.effort : base.effort,
      startMode: row.startMode === "plan" ? "plan" : "normal",
      // The default is to skip. Only an explicit false asks for approval: defaulting a missing
      // or corrupt value to "ask" would leave every session on a device that read older prefs
      // waiting for approval, and not changing behaviour is the premise of this feature.
      skipPermissions: row.skipPermissions !== false,
    };
  }
  return out;
}

export function agentLaunchDefault(s: Settings, kind: string): AgentLaunchDefault {
  return s.agentLaunchDefaults[kind] || { model: "", effort: "", startMode: "normal", skipPermissions: true };
}

// expandThinking is a kind's "expand thinking from the start" setting. Unset, an unknown kind,
// and a corrupt saved value (server ui-prefs are written by other Console versions too) all
// mean false, i.e. shown collapsed.
export function expandThinking(s: Settings, kind?: string | null): boolean {
  if (!kind) return false;
  return s.expandThinking?.[kind] === true;
}

export type StreamRepliesMode = "off" | "lines" | "typewriter";

// streamReplies is a kind's "stream replies in the chat view" setting. Unset, `true` (the switch
// it used to be, left on) and a corrupt saved value all mean typewriter; only an explicit false
// or "off" turns it off, so a member who switched it off before the third mode existed stays off.
export function streamReplies(s: Settings, kind?: string | null): StreamRepliesMode {
  if (!kind) return "off";
  const v = s.streamReplies?.[kind];
  if (v === false || v === "off") return "off";
  if (v === "lines") return "lines";
  return "typewriter";
}

// migrateOpencodeCatalog maps the legacy menu-shaping values onto the billing-route
// choice. Kept exported for the load() path and the tests — the Agent applies the same
// rule server-side (opencode.CatalogPref), so the two never disagree.
export function migrateOpencodeCatalog(v: unknown): "off" | "own" | "free" | "go" | "zen" {
  if (v === "off" || v === "own" || v === "free" || v === "go" || v === "zen") return v;
  if (v === "hide-zen") return "go"; // hiding Zen means intending to use Go only
  if (v === "go-first" || v === "all") return "zen"; // both mean wanting to see both
  return "off"; // unset/unknown = disabled until explicitly chosen
}
