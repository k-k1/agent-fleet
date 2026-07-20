// Per-agent launch model options for the launch dialogs (LaunchModal /
// NewSessionModal). claude keeps a fixed tier-alias list (settings.ts — aliases
// track releases, so it can't go stale); codex and opencode have LIVE catalogs
// served by the Agent (GET /agents/{kind}/models — codex: `codex debug models`
// under its own subscription auth, opencode: `opencode models` reflecting the
// user's connected providers). Codex is cached once per Console load. OpenCode
// is refetched on each picker mount because its provider connections can change
// from Settings during the same Console load (the Agent cheaply caches the CLI
// call). Until the fetch lands (or when it fails) the picker offers only 既定,
// which launches the CLI on its own default model.
import { useEffect, useState } from "react";
import { api } from "../core/api/client.ts";
import { CLAUDE_MODELS } from "./settings.ts";
import { t } from "./i18n/index.ts";

export type ModelOption = [string, string]; // [value sent as `model`, display label]
export type EffortOption = [string, string];

interface ModelDescriptor {
  id: string;
  label: string;
  efforts: string[];
  defaultEffort: string;
}

// Resolved lazily (not a module-level constant) so the "既定" label reflects the
// current locale and updates on language switch.
const defaultOnly = (): ModelOption[] => [["", t("ui.default")]];
const isDynamic = (kind: string) => kind === "codex" || kind === "opencode" || kind === "agy";
const cache = new Map<string, ModelOption[]>();
const descriptors = new Map<string, ModelDescriptor[]>();
const inflight = new Map<string, Promise<ModelOption[]>>();

function fetchModels(kind: string): Promise<ModelOption[]> {
  const cacheable = kind !== "opencode";
  const hit = cacheable ? cache.get(kind) : undefined;
  if (hit) return Promise.resolve(hit);
  let p = inflight.get(kind);
  if (!p) {
    p = api(`api/agents/${kind}/models`)
      .then((d) => {
        const items: { id?: string; label?: string; efforts?: unknown; defaultEffort?: unknown }[] = Array.isArray(d?.models)
          ? d.models
          : [];
        const desc = items
          .filter((m) => m && typeof m.id === "string" && m.id)
          .map((m): ModelDescriptor => ({
            id: m.id!,
            label: m.label || m.id!,
            efforts: Array.isArray(m.efforts) ? m.efforts.filter((x): x is string => typeof x === "string" && !!x) : [],
            defaultEffort: typeof m.defaultEffort === "string" ? m.defaultEffort : "",
          }));
        const opts = desc.map((m): ModelOption => [m.id, m.label]);
        if (!opts.length) throw new Error("empty"); // workspace stopped / CLI absent — retry next open
        const full = [...defaultOnly(), ...opts];
        descriptors.set(kind, desc);
        if (cacheable) cache.set(kind, full);
        else inflight.delete(kind);
        return full;
      })
      .catch(() => {
        inflight.delete(kind);
        return defaultOnly();
      });
    inflight.set(kind, p);
  }
  return p;
}

const FALLBACK_EFFORTS: Record<string, string[]> = {
  claude: ["low", "medium", "high", "xhigh", "max"],
  codex: ["minimal", "low", "medium", "high", "xhigh"],
  opencode: ["low", "medium", "high", "max"],
};

// useEffortOptions returns model-aware effort choices when the live Codex catalog
// provides them. Older Codex/OpenCode catalogs lack metadata, so a compatibility
// list remains available rather than making the managed UI unusable.
export function useEffortOptions(kind: string, model: string): EffortOption[] {
  const [version, setVersion] = useState(0);
  useEffect(() => {
    if (!isDynamic(kind)) return;
    let alive = true;
    void fetchModels(kind).then(() => alive && setVersion((v) => v + 1));
    return () => {
      alive = false;
    };
  }, [kind]);
  void version;
  const rows = descriptors.get(kind) || [];
  const selected = rows.find((m) => m.id === model);
  const efforts = kind === "claude" && model === "haiku"
    ? []
    : selected?.efforts.length
    ? selected.efforts
    : [...new Set(rows.flatMap((m) => m.efforts))].length
      ? [...new Set(rows.flatMap((m) => m.efforts))]
      : FALLBACK_EFFORTS[kind] || [];
  // claude は CLI から per-model の既定 effort を取得できない（catalog を持たない）。
  // 未指定時は Claude Code の CLI 既定（xhigh）に落ちるので、それを注記する。
  // codex/opencode はカタログの defaultEffort をそのまま「既定（medium）」等で表示。
  const def = selected?.defaultEffort || "";
  const defaultLabel = def
    ? t("ui.default_with", { effort: def })
    : kind === "claude" && efforts.length
      ? t("ui.default_claude_xhigh")
      : t("ui.default");
  return [["", defaultLabel], ...efforts.map((e): EffortOption => [e, e])];
}

// useModelOptions returns the launch model choices for `kind` — null when the kind
// has no picker (caps.model false). Dynamic kinds resolve asynchronously: 既定-only
// first, the full list once fetched.
export function useModelOptions(kind: string): ModelOption[] | null {
  const [opts, setOpts] = useState<ModelOption[]>(() => cache.get(kind) || defaultOnly());
  useEffect(() => {
    if (!isDynamic(kind)) return;
    let alive = true;
    setOpts(cache.get(kind) || defaultOnly()); // reset stale options from a previous kind
    void fetchModels(kind).then((l) => alive && setOpts(l));
    return () => {
      alive = false;
    };
  }, [kind]);
  if (kind === "claude") return CLAUDE_MODELS;
  if (isDynamic(kind)) return opts;
  return null;
}
