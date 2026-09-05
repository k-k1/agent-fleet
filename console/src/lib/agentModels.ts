// Per-agent launch model options for the launch dialogs (LaunchModal /
// NewSessionModal). claude keeps a fixed tier-alias list (settings.ts — aliases
// track releases, so it can't go stale); codex and opencode have LIVE catalogs
// served by the Agent (GET /agents/{kind}/models — codex: `codex debug models`
// under its own subscription auth, opencode: `opencode models` reflecting the
// user's connected providers). Codex is cached once per Console load. OpenCode
// is refetched on each picker mount because its provider connections can change
// from Settings during the same Console load (the Agent cheaply caches the CLI
// call). Until the fetch lands (or when it fails) the picker offers only Default,
// which launches the CLI on its own default model.
import { useEffect, useState } from "react";
import { api } from "../core/api/client.ts";
import { CLAUDE_MODELS, useSettings } from "./settings.ts";
import { hiddenModelsFor, isModelHidden, modelMatchesHidden } from "./modelDeny.ts";
import { t } from "./i18n/index.ts";

export type ModelOption = [string, string]; // [value sent as `model`, display label]
export type EffortOption = [string, string];

interface ModelDescriptor {
  id: string;
  label: string;
  efforts: string[];
  defaultEffort: string;
}

// Resolved lazily (not a module-level constant) so the "Default" label reflects the
// current locale and updates on language switch.
const defaultOnly = (): ModelOption[] => [["", t("ui.default")]];
const isDynamic = (kind: string) =>
  kind === "codex" || kind === "opencode" || kind === "agy" || kind === "copilot" || kind === "cursor" || kind === "kiro";
const cache = new Map<string, ModelOption[]>();
const descriptors = new Map<string, ModelDescriptor[]>();
const inflight = new Map<string, Promise<ModelOption[]>>();

// opencode ships the SAME model under two opencode.ai billing routes — opencode/… is
// Zen (pay-per-request from a balance) and opencode-go/… is the Go subscription — and
// 10 of the 16 Go models collide by name. The id says which route it is, but reading a
// prefix off a dropdown is not something a user should have to do, so the picker spells
// it out: every Go entry is marked, and a metered entry is marked only when a Go twin
// exists (marking all ~59 Zen ids would be noise). Localized here rather than server
// side so the label follows the Console language.
function decorateLabel(kind: string, label: string, all: ModelDescriptor[]): string {
  if (kind !== "opencode") return label;
  const GO = "opencode-go/";
  const ZEN = "opencode/";
  if (label.startsWith(GO)) return t("agents.oc_model_go", { model: label.slice(GO.length) });
  if (label.startsWith(ZEN)) {
    const name = label.slice(ZEN.length);
    const twin = all.some((m) => m.id === GO + name);
    if (twin) return t("agents.oc_model_zen", { model: name });
  }
  return label;
}

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
        const opts = desc.map((m): ModelOption => [m.id, decorateLabel(kind, m.label, desc)]);
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
  copilot: ["minimal", "low", "medium", "high", "xhigh", "max"],
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
  // copilot's Auto (the only model on Free, and copilot's default) rejects --effort with
  // "Model auto does not support reasoning effort configuration". Offer the default effort only
  // until a concrete non-auto model is picked, matching the backend's launch guard.
  const noEffort =
    (kind === "claude" && model === "haiku") ||
    (kind === "copilot" && (model === "" || model === "auto"));
  // Union of the efforts across the catalog (the fallback when the selected model has no
  // metadata).
  const catalogEfforts = [...new Set(rows.flatMap((m) => m.efforts))];
  const efforts = noEffort
    ? []
    : selected?.efforts.length
    ? selected.efforts
    : catalogEfforts.length
      ? catalogEfforts
      : FALLBACK_EFFORTS[kind] || [];
  // claude cannot report a per-model default effort through the CLI (it has no catalog). With
  // none given it falls back to the Claude Code CLI default (xhigh), so say so. codex/opencode
  // show the catalog's defaultEffort as-is, e.g. "Default (medium)".
  const def = selected?.defaultEffort || "";
  const defaultLabel = def
    ? t("ui.default_with", { effort: def })
    : kind === "claude" && efforts.length
      ? t("ui.default_claude_xhigh")
      : t("ui.default");
  return [["", defaultLabel], ...efforts.map((e): EffortOption => [e, e])];
}

// useModelOptions returns the launch model choices for `kind` — null when the kind
// has no picker (caps.model false). Dynamic kinds resolve asynchronously: Default-only
// first, the full list once fetched.
export function useModelOptions(kind: string): ModelOption[] | null {
  const [opts, setOpts] = useState<ModelOption[]>(() => cache.get(kind) || defaultOnly());
  // The opencode catalog is SHAPED server-side by this preference (Go first / hide the
  // metered twins), so a change has to refetch — otherwise the picker keeps showing the
  // old list until the Console is reloaded.
  const s = useSettings();
  const catalogPref = s.opencodeCatalog;
  const pref = kind === "opencode" ? catalogPref : "";
  useEffect(() => {
    if (!isDynamic(kind)) return;
    let alive = true;
    setOpts(cache.get(kind) || defaultOnly()); // reset stale options from a previous kind
    void fetchModels(kind).then((l) => alive && setOpts(l));
    return () => {
      alive = false;
    };
  }, [kind, pref]);
  // Drop the models the user hides (settings.hiddenModels). The Agent filters
  // /agents/{kind}/models with the same setting, but claude's fixed list lives in the Console
  // (CLAUDE_MODELS) and never goes through that fetch, so filter here too. Dynamic kinds are
  // filtered as well, because a fetched cache can be older than the setting change.
  if (kind === "claude") {
    const custom = s.claudeCustomModels.map((id): ModelOption => [id, id]);
    return visibleModelOptions(s.hiddenModels, kind, [...CLAUDE_MODELS, ...custom]);
  }
  if (isDynamic(kind)) return visibleModelOptions(s.hiddenModels, kind, opts);
  return null;
}

// useModelCatalogSettled answers whether this kind's catalog fetch has settled once. Static kinds
// (claude) are always true.
//
// It exists only to gate the "no models available" note, and must be false while the fetch is in
// flight: useModelOptions returns Default alone until it resolves, so writing "Default only"
// without checking settled always flashes that note right after opening.
//
// fetchModels folds duplicates through cache / inflight, so calling it for the same kind as
// useModelOptions still fetches once.
export function useModelCatalogSettled(kind: string): boolean {
  const [settled, setSettled] = useState(() => !isDynamic(kind) || cache.has(kind));
  useEffect(() => {
    if (!isDynamic(kind)) {
      setSettled(true);
      return;
    }
    let alive = true;
    setSettled(cache.has(kind));
    void fetchModels(kind).then(() => alive && setSettled(true));
    return () => {
      alive = false;
    };
  }, [kind]);
  return settled;
}

// visibleModelOptions drops hidden models from the choices. "" (Default) is not an id, so it
// always stays.
function visibleModelOptions(
  hiddenModels: Record<string, string[]> | undefined,
  kind: string,
  options: ModelOption[],
): ModelOption[] {
  const ids = options.map(([id]) => id).filter(Boolean);
  const hidden = hiddenModelsFor(hiddenModels, kind, kind === "claude" ? ids : undefined);
  if (!hidden.length) return options;
  return options.filter(([id]) => !id || !hidden.some((h) => modelMatchesHidden(id, h)));
}

// useHiddenModel asks whether a stored selection is hidden. It exists so the existing rescue that
// adds a value missing from the catalog back into the choices (ModelPicker / AssistantTab) is not
// applied to hidden models: adding one back would resurrect a model the user hid.
export function useHiddenModel(kind: string, model: string): boolean {
  const hiddenModels = useSettings().hiddenModels;
  const catalog = kind === "claude" ? CLAUDE_MODELS.map(([id]) => id) : undefined;
  return isModelHidden(hiddenModels, kind, model, catalog);
}
