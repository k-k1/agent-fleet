// Session kind → display icon (codicon name) + label. Thin wrappers over the
// agent registry (src/agents/registry) so the kind presentation lives in one
// place. Shared by the Sessions list, the Repos launch menu, the New Session
// modal, and the archive modal. Kept as named helpers for existing call sites;
// new code can read the descriptor directly via agentOf(kind).
import { AGENTS, agentOf } from "../agents/registry.ts";
import { SESSION_KINDS } from "../types/session.ts";

export const kindIcon = (k: string | null | undefined): string => agentOf(k).icon;
export const kindLabel = (k: string | null | undefined): string => agentOf(k).label;
// Full product name for the roomy launch pickers only ("start work" 「作業を始める」 /
// the "get started" hub 「はじめる」);
// everywhere else keeps the compact kindLabel. Falls back to label when an agent
// declares no separate display name.
export const kindDisplayName = (k: string | null | undefined): string => {
  const a = agentOf(k);
  return a.displayName || a.label;
};
// 2-char abbreviation for tight spots (narrow pane headers): claude=cc, codex=cx,
// opencode=oc, shell=sh, ssm=aw (AWS SSM).
export const kindShort = (k: string | null | undefined): string => agentOf(k).short;
// Canonical kind slug for CSS color classes (.kind-<slug>).
export const kindClass = (k: string | null | undefined): string => agentOf(k).cssClass;

const COLORED_SLUGS = new Set(SESSION_KINDS.map((k) => AGENTS[k].cssClass));

// The class list that colours an element by a kind slug: `kind-<slug>` sets --kind
// (styles/tokens.css) and the `kind` marker is what the colouring rules select on. A slug with
// no registered colour (the generic descriptor's, or a raw kind the Agent sent that this Console
// does not know) gets no marker, so the element keeps its uncoloured base style.
export const kindColorClasses = (slug: string): string => (COLORED_SLUGS.has(slug) ? "kind kind-" : "kind-") + slug;

// kindColorClasses for a session kind (through the registry, as kindClass).
export const kindClasses = (k: string | null | undefined): string => kindColorClasses(kindClass(k));
