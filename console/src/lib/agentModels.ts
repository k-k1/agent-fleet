// Per-agent launch model options for the launch dialogs (LaunchModal /
// NewSessionModal). claude keeps a fixed tier-alias list (settings.ts — aliases
// track releases, so it can't go stale); codex and opencode have LIVE catalogs
// served by the Agent (GET /agents/{kind}/models — codex: `codex debug models`
// under its own subscription auth, opencode: `opencode models` reflecting the
// user's connected providers), fetched once per Console load and cached
// module-level (the Agent caches the CLI call too). Until the fetch lands (or
// when it fails) the picker offers only 既定, which launches the CLI on its own
// default model.
import { useEffect, useState } from "react";
import { api } from "../core/api/client.ts";
import { CLAUDE_MODELS } from "./settings.ts";

export type ModelOption = [string, string]; // [value sent as `model`, display label]

const DEFAULT_ONLY: ModelOption[] = [["", "既定"]];
const isDynamic = (kind: string) => kind === "codex" || kind === "opencode";
const cache = new Map<string, ModelOption[]>();
const inflight = new Map<string, Promise<ModelOption[]>>();

function fetchModels(kind: string): Promise<ModelOption[]> {
  const hit = cache.get(kind);
  if (hit) return Promise.resolve(hit);
  let p = inflight.get(kind);
  if (!p) {
    p = api(`api/agents/${kind}/models`)
      .then((d) => {
        const items: { id?: string; label?: string }[] = Array.isArray(d?.models) ? d.models : [];
        const opts = items
          .filter((m) => m && typeof m.id === "string" && m.id)
          .map((m): ModelOption => [m.id!, m.label || m.id!]);
        if (!opts.length) throw new Error("empty"); // workspace stopped / CLI absent — retry next open
        const full = [...DEFAULT_ONLY, ...opts];
        cache.set(kind, full);
        return full;
      })
      .catch(() => {
        inflight.delete(kind);
        return DEFAULT_ONLY;
      });
    inflight.set(kind, p);
  }
  return p;
}

// useModelOptions returns the launch model choices for `kind` — null when the kind
// has no picker (caps.model false). Dynamic kinds resolve asynchronously: 既定-only
// first, the full list once fetched.
export function useModelOptions(kind: string): ModelOption[] | null {
  const [opts, setOpts] = useState<ModelOption[]>(() => cache.get(kind) || DEFAULT_ONLY);
  useEffect(() => {
    if (!isDynamic(kind)) return;
    let alive = true;
    setOpts(cache.get(kind) || DEFAULT_ONLY); // reset stale options from a previous kind
    void fetchModels(kind).then((l) => alive && setOpts(l));
    return () => {
      alive = false;
    };
  }, [kind]);
  if (kind === "claude") return CLAUDE_MODELS;
  if (isDynamic(kind)) return opts;
  return null;
}
