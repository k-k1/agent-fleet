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

// Resolved lazily (not a module-level constant) so the "既定" label reflects the
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
  // copilot の Auto（Free の唯一のモデル / copilot 既定）は --effort を拒否する
  // （"Model auto does not support reasoning effort configuration"）。concrete な
  // 非 auto モデルが選ばれるまで effort は既定のみにする（バックエンドの起動ガードと一致）。
  const noEffort =
    (kind === "claude" && model === "haiku") ||
    (kind === "copilot" && (model === "" || model === "auto"));
  // カタログ全体の effort 和集合（選択モデルにメタデータが無いときの次善候補）。
  const catalogEfforts = [...new Set(rows.flatMap((m) => m.efforts))];
  const efforts = noEffort
    ? []
    : selected?.efforts.length
    ? selected.efforts
    : catalogEfforts.length
      ? catalogEfforts
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
  // 使わないモデル（settings.hiddenModels）を落とす。Agent 側は同じ設定で
  // /agents/{kind}/models を絞っているが、claude だけは固定リストを Console が
  // 直接持っていてフェッチを通らない（CLAUDE_MODELS）ので、ここでも掛ける。動的 kind
  // にも掛けるのは、フェッチ済みキャッシュが設定変更より古いことがあるための保険。
  if (kind === "claude") {
    const custom = s.claudeCustomModels.map((id): ModelOption => [id, id]);
    return visibleModelOptions(s.hiddenModels, kind, [...CLAUDE_MODELS, ...custom]);
  }
  if (isDynamic(kind)) return visibleModelOptions(s.hiddenModels, kind, opts);
  return null;
}

// useModelCatalogSettled は「この kind のカタログ取得が一度決着したか」。静的 kind
// （claude）は常に true。
//
// 「選べるモデルが 1 つも無い」という注記を出すためだけの状態で、**取得中は false** に
// する必要がある: useModelOptions は解決するまで 既定 だけを返すので、settled を見ずに
// 「既定のみ」と書くと、開いた直後に必ず一瞬出てから消える。
//
// fetchModels は cache / inflight で重複を畳むので、useModelOptions と同じ kind で
// 二重に呼んでも取得は 1 回きり。
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

// visibleModelOptions は選択肢から除外モデルを落とす。"" （既定）は id ではないので常に残す。
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

// useHiddenModel は「保存済みの選択値が除外されているか」を問う。カタログから消えた
// 値を選択肢に足し戻す既存の救済（ModelPicker / AssistantTab）を、除外モデルにだけは
// 適用しないために使う — 足し戻すと隠したはずのモデルが復活する。
export function useHiddenModel(kind: string, model: string): boolean {
  const hiddenModels = useSettings().hiddenModels;
  const catalog = kind === "claude" ? CLAUDE_MODELS.map(([id]) => id) : undefined;
  return isModelHidden(hiddenModels, kind, model, catalog);
}
