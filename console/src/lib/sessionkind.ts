// Session kind → display icon (codicon name) + label. Thin wrappers over the
// agent registry (src/agents/registry) so the kind presentation lives in one
// place. Shared by the Sessions list, the Repos launch menu, the New Session
// modal, and the archive modal. Kept as named helpers for existing call sites;
// new code can read the descriptor directly via agentOf(kind).
import { agentOf } from "../agents/registry.ts";

export const kindIcon = (k: string | null | undefined): string => agentOf(k).icon;
export const kindLabel = (k: string | null | undefined): string => agentOf(k).label;
// 2-char abbreviation for tight spots (narrow pane headers): claude=cc, codex=cx,
// opencode=oc, shell=sh, ssm=aw (AWS SSM).
export const kindShort = (k: string | null | undefined): string => agentOf(k).short;
// Canonical kind slug for CSS color classes (.kind-<slug>).
export const kindClass = (k: string | null | undefined): string => agentOf(k).cssClass;
