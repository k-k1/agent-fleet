// Per-agent launch model options for the launch dialogs (LaunchModal /
// NewSessionModal). claude and codex have fixed catalogs (settings.ts); opencode's
// depends on which providers the user has connected, so it's fetched live from the
// Agent (api/agents/opencode/models) and cached module-level — one fetch per Console
// load is enough (the Agent caches the CLI call too). Until the fetch lands (or when
// it fails) the picker offers only 既定, which launches opencode on its own default.
import { useEffect, useState } from "react";
import { api } from "../core/api/client.ts";
import { CLAUDE_MODELS, CODEX_MODELS } from "./settings.ts";

export type ModelOption = [string, string]; // [value sent as `model`, display label]

const OC_DEFAULT: ModelOption[] = [["", "既定"]];
let ocCache: ModelOption[] | null = null;
let ocFetch: Promise<ModelOption[]> | null = null;

function fetchOpencodeModels(): Promise<ModelOption[]> {
  if (ocCache) return Promise.resolve(ocCache);
  if (!ocFetch)
    ocFetch = api("api/agents/opencode/models")
      .then((d) => {
        const ids: string[] = Array.isArray(d?.models) ? d.models : [];
        if (!ids.length) throw new Error("empty"); // workspace stopped / CLI absent — retry next open
        ocCache = [...OC_DEFAULT, ...ids.map((id): ModelOption => [id, id])];
        return ocCache;
      })
      .catch(() => {
        ocFetch = null;
        return OC_DEFAULT;
      });
  return ocFetch;
}

// useModelOptions returns the launch model choices for `kind` — null when the kind
// has no picker (caps.model false). opencode resolves asynchronously: 既定-only
// first, the full list once fetched.
export function useModelOptions(kind: string): ModelOption[] | null {
  const [oc, setOc] = useState<ModelOption[]>(ocCache || OC_DEFAULT);
  useEffect(() => {
    if (kind !== "opencode") return;
    let alive = true;
    void fetchOpencodeModels().then((l) => alive && setOc(l));
    return () => {
      alive = false;
    };
  }, [kind]);
  if (kind === "claude") return CLAUDE_MODELS;
  if (kind === "codex") return CODEX_MODELS;
  if (kind === "opencode") return oc;
  return null;
}
