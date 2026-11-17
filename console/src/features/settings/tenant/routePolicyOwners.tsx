// The names the Model access pages give the owners of routes (harnesses, the deployment engine's
// image wires) when they list what members can use (routePolicyOverview.tsx). Members' own screens
// never name a layer or a hidden item.
import { useT } from "../../../lib/i18n/index.ts";
import { SESSION_KINDS } from "../../../types/session.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";

type Tr = ReturnType<typeof useT>;

// Image providers that are not agent kinds: the deployment engine's two wires.
const PROVIDER_KEY = {
  comfy: "admin.mpol_hidden_provider_comfy",
  "openai-compat": "admin.mpol_hidden_provider_openai_compat",
} as const;

// kindDisplayName falls back to Claude for a kind it does not know, so an id outside the
// registry is shown as itself rather than under another harness's name.
export function ownerName(tr: Tr, id: string): string {
  if (Object.hasOwn(PROVIDER_KEY, id)) return tr(PROVIDER_KEY[id as keyof typeof PROVIDER_KEY]);
  return (SESSION_KINDS as readonly string[]).includes(id) ? kindDisplayName(id) : id;
}
