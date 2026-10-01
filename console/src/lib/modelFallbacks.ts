// The Console's one owned place for concrete model ids (issue #1084). Model ids move every few
// weeks; an id pinned somewhere nobody owns goes on naming a retired model long after the
// catalog has moved on. Each entry says who owns it, where it came from and why the live
// catalog cannot supply it. scripts/model-id-lint.mjs fails CI on a model-id-shaped literal
// anywhere else in src/; the Agent's counterpart is workspace/agent/internal/modelfallback.

/** Placeholder of the "registered Claude models" input (Settings > Agents > Claude). */
export const CLAUDE_CUSTOM_MODEL_PLACEHOLDER = "claude-opus-4-8";

export interface ModelFallbackEntry {
  id: string;
  /** The agent kind the id belongs to. */
  kind: string;
  /** The feature that reads it — the place to ask before changing it. */
  owner: string;
  /** Where the value came from. */
  source: string;
  /** Why the live catalog cannot supply it. */
  whyNotDiscovered: string;
}

export const MODEL_FALLBACKS: readonly ModelFallbackEntry[] = [
  {
    id: CLAUDE_CUSTOM_MODEL_PLACEHOLDER,
    kind: "claude",
    owner: "Settings > Agents > Claude, registered models (AgentCardParts.tsx)",
    source: "an example of the full-id syntax the field accepts (feat(console) 4fac51c2a); it is never sent anywhere",
    whyNotDiscovered:
      "Claude Code over OAuth has no account-linked catalog; the field exists to name ids the catalog cannot list",
  },
];
