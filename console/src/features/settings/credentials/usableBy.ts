import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { allowedRoutesOf, type LiveKind } from "./memberCatalog.ts";

// "Usable by" (ADR 0138 D6): which harnesses can take a credential, computed from the live
// catalogue. It is read-only and says what is possible now; choosing which of them may use a
// shared key ("Use for", D3) is M3 and is not drawn here.

/** How one card's credential is used: the route it opens, the harnesses able to take a credential
 *  of that kind, and whether only one harness can because its CLI keeps the login (or its own copy
 *  of the key) to itself. */
export interface CredentialUse {
  routes: string[];
  kinds: string[];
  /** "cli": a CLI-locked login. "copy": the harness keeps its own copy of the key. */
  locked?: "cli" | "copy";
}

/** Each Model credentials card's credential, by the card's id. */
export const CREDENTIAL_USE: Record<string, CredentialUse> = {
  claude: { routes: ["anthropic-subscription"], kinds: ["claude"], locked: "cli" },
  codex: { routes: ["chatgpt-subscription", "openai-api"], kinds: ["codex"], locked: "copy" },
  cursor: { routes: ["cursor-login"], kinds: ["cursor"], locked: "cli" },
  kiro: { routes: ["kiro-login"], kinds: ["kiro"], locked: "cli" },
  agy: { routes: ["agy-login"], kinds: ["agy"], locked: "cli" },
  muse: { routes: ["muse-login", "meta-api"], kinds: ["muse"], locked: "cli" },
  copilot: { routes: ["copilot-login"], kinds: ["copilot"] },
  opencode: { routes: ["opencode-account", "opencode-go", "opencode-zen"], kinds: ["opencode"] },
  lcpp: { routes: ["member-llamacpp"], kinds: ["lcpp"] },
  // The provider keys share opencode's slot; claude's anthropic-api reads the Anthropic one too.
  anthropic: { routes: ["anthropic-api"], kinds: ["claude", "opencode"] },
  openai: { routes: ["openai-api"], kinds: ["opencode"] },
  google: { routes: ["gemini-api"], kinds: ["opencode"] },
  "provider-keys": { routes: ["openrouter-api", "sakana-api", "custom-api"], kinds: ["opencode"] },
  "amazon-bedrock": { routes: ["bedrock"], kinds: ["claude", "opencode"] },
  "af-engine": { routes: ["af-engine-llm"], kinds: ["lcpp", "opencode"] },
};

/** The harnesses that can take the credential now: those able to, which the catalogue lists with
 *  one of its routes allowed. */
export function usableBy(use: CredentialUse | undefined, kinds: Map<string, LiveKind> | null): string[] {
  if (!use || !kinds) return [];
  return use.kinds.filter((k) => {
    const allowed = allowedRoutesOf(kinds.get(k));
    return kinds.has(k) && use.routes.some((r) => allowed.has(r));
  });
}

type Tr = (key: never, vars?: Record<string, string | number>) => string;

/** The line itself, or "" when no harness can take the credential now. */
export function usableByLine(tr: Tr, use: CredentialUse | undefined, kinds: string[]): string {
  if (!use || kinds.length === 0) return "";
  const t = tr as unknown as (key: string, vars?: Record<string, string | number>) => string;
  if (use.locked === "cli" && kinds.length === 1) return t("cred.only_cli", { name: kindDisplayName(kinds[0]) });
  if (use.locked === "copy" && kinds.length === 1) return t("cred.only_copy", { name: kindDisplayName(kinds[0]) });
  return t("cred.usable_by", { names: kinds.map((k) => kindDisplayName(k)).join(", ") });
}
