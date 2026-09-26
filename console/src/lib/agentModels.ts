// Per-agent launch model options for the launch dialogs (LaunchModal /
// NewSessionModal). claude keeps a fixed tier-alias list (settings.ts — aliases
// track releases, so it can't go stale); codex and opencode have LIVE catalogs
// served by the Agent (GET /agents/{kind}/models — codex: `codex debug models`
// under its own subscription auth, opencode: `opencode models` reflecting the
// user's connected providers). Codex is cached once per Console load. OpenCode
// and lcpp are refetched on each picker mount because their connections can change
// from Settings during the same Console load (VOLATILE_MODEL_KINDS below; the Agent
// cheaply caches both answers). Until the fetch lands (or when it fails) the picker
// offers only Default, which launches the CLI on its own default model.
import { useEffect, useState } from "react";
import { api, getTenant, getUser, isTransientErr } from "../core/api/client.ts";
import { CLAUDE_MODELS, getSettings, useSettings } from "./settings.ts";
import { hiddenModelsFor, isModelHidden, modelMatchesHidden } from "./modelDeny.ts";
import { t } from "./i18n/index.ts";

export type ModelOption = [string, string]; // [value sent as `model`, display label]
export type EffortOption = [string, string];

interface ModelDescriptor {
  id: string;
  label: string;
  /** Provider id of the company that MADE the model ("anthropic", "zhipuai"), resolved by the
   *  Agent (workspace/agent/model_provider.go). "" when it could not be placed. Kept out of
   *  ModelOption so the dozen call sites that destructure [value, label] stay untouched. */
  provider: string;
  efforts: string[];
  defaultEffort: string;
}

// requiresConcreteModel names the one dynamic kind (today: lcpp) whose catalog has no "let the
// CLI pick" entry. This is NOT because llama-server itself demands a model name on every
// request — measured live (docs/log/107's 2026-09-21 addendum), a single-model instance
// ignores the request's own `model` field entirely (any string, or none, answers 200) — it is
// the Agent's own choice to require one: a router deployment (`--models-max`, ADR 0093
// decision 7's own measured `role: "router"`) DOES dispatch on this field, the id is what pins
// the session to one entry of the model list the launch menu itself is built from (GET
// /agents/lcpp/models — there is no vendor "tier alias" the way claude has), and it is the
// only record of which model a conversation believes it is talking to (the same drift a member
// swapping the LAN box under an unchanged URL can otherwise hide). Checked by every launch
// entry point (LaunchModal, StartModal, quick launch, the Settings default row) so none of them
// can send an empty model for this kind (docs/log/109).
export function requiresConcreteModel(kind: string): boolean {
  return kind === "lcpp";
}

// Resolved lazily (not a module-level constant) so the "Default" label reflects the
// current locale and updates on language switch. A kind with no CLI-picked default gets no
// Default entry at all — offering one would be a choice that can never actually be launched.
const defaultOnly = (kind: string): ModelOption[] => (requiresConcreteModel(kind) ? [] : [["", t("ui.default")]]);
const isDynamic = (kind: string) =>
  kind === "codex" ||
  kind === "opencode" ||
  kind === "agy" ||
  kind === "copilot" ||
  kind === "cursor" ||
  kind === "kiro" ||
  kind === "lcpp" ||
  kind === "muse";
// Per kind, the options last fetched and the identity they were fetched under (catalogIdent):
// a hit counts only while that identity still holds.
const cache = new Map<string, { ident: string; opts: ModelOption[] }>();
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

// The opencode billing route the Agent ACTUALLY shaped the last list with (the `route` member
// of GET /agents/opencode/models). It equals the selected route except where Catalog's
// empty-menu rescue had to ignore it — selecting Go on an account with no Go contract lists
// the METERED ids instead, which is the one thing a billing setting must not do quietly. ""
// while no list has been fetched, and after a failed fetch: nothing is known then, and a
// warning drawn from nothing is worse than none.
let opencodeRoute = "";

// Why the last fetch for this kind came back with no model: either as the Agent named it
// ("catalog_empty" | "route" | "hidden" — agent_models.go's emptyReason) or "unreachable",
// which is this side's own answer for "the Agent never got to say". The picker used to have
// to guess out loud ("check the connection and the plan"), which is wrong advice for three of
// the four. Cleared as soon as a list arrives.
const emptyReasons = new Map<string, string>();

export function modelCatalogReason(kind: string): string {
  return emptyReasons.get(kind) || "";
}

/** Retry intervals (ms) for a catalog fetch that could not reach the Agent — the same policy
 *  and the same reason as connsRetry's: right after a workspace starts, the Agent is not
 *  listening yet and the CP answers 502. Taking that as "this account has no model" is how the
 *  picker came to advise checking a connection and a plan that were never the problem. ~22s in
 *  total; a boot slower than that is picked up when the modal is opened again (a failure
 *  caches nothing). */
export const MODELS_RETRY_MS = [1500, 3000, 6000, 12000];

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

/** One request. Resolves to the parsed body, or null when it did not land — a thrown fetch, or
 *  a transient backend error (`http_502` while the workspace boots, any 5xx body). An empty
 *  `models` array is NOT this case: that is an answer, and it carries its own reason. */
async function requestModels(kind: string): Promise<Record<string, unknown> | null> {
  const d = await api(`api/agents/${kind}/models`).catch(() => null);
  if (!d || isTransientErr(d) || !Array.isArray(d.models)) return null;
  return d;
}

/** Kinds whose model list can change from Settings DURING one Console load, so their answer is
 *  never cached here and every picker mount asks the Agent again.
 *
 *  - opencode: the provider connections (and the billing route) are edited on its own card.
 *  - lcpp: the member's own llama.cpp connection (docs/log/107). The list is whatever THAT
 *    server's GET /v1/models answers, so saving, changing or clearing the endpoint changes it
 *    at once — and a cached copy is the deployment engine's list still on screen after the
 *    member pointed lcpp somewhere else, which is how this was found.
 *
 *  Both are cheap to re-ask: the Agent caches its own side (lcpp for 30 s, engines.go's
 *  lcppMemberModelsCacheTTL, and it drops that cache when the connection is saved or deleted). */
const VOLATILE_MODEL_KINDS = new Set(["opencode", "lcpp"]);

/** What a kind's list depends on besides the kind: whose it is (the Console switches tenant and
 *  user within one page load) and the settings the Agent shapes it with — hidden models, and
 *  opencode's billing route. A list fetched under another identity is another list: before this
 *  was part of the key, un-hiding a model never brought it back until a reload, because the
 *  cached list was the one the Agent had filtered (#972 review, round 3). */
function catalogIdent(kind: string): string {
  const s = getSettings();
  return [
    getTenant(),
    getUser(),
    JSON.stringify(hiddenModelsFor(s.hiddenModels, kind)),
    kind === "opencode" ? s.opencodeCatalog : "",
  ].join("|");
}

/** The kind's hidden-models entry as this tab holds it, in the form the Agent echoes it back
 *  (`appliedHidden`: the non-empty strings, in order, verbatim). */
function localHidden(kind: string): string {
  const raw = getSettings().hiddenModels?.[kind];
  const list = Array.isArray(raw) ? raw.filter((v): v is string => typeof v === "string" && !!v.trim()) : [];
  return JSON.stringify(list);
}

/** Was this answer computed under `hidden` (a localHidden value)? The Agent reads the setting
 *  from its ui-prefs, which this tab updates 600 ms after a change — or not at all before its
 *  first read of the server copy, or when the save fails — so an answer can reflect a list the
 *  screen no longer holds. Only a matching answer may be kept (#972 review, round 4: waiting for
 *  the save instead guessed, and each wrong guess was a stale answer cached under a new key).
 *  An Agent too old to say is taken at its word. */
function answeredUnder(d: Record<string, unknown> | null, hidden: string): boolean {
  if (!d || !Array.isArray(d.appliedHidden)) return true;
  return JSON.stringify(d.appliedHidden) === hidden;
}

function cachedOptions(kind: string): ModelOption[] | undefined {
  const hit = cache.get(kind);
  return hit && hit.ident === catalogIdent(kind) ? hit.opts : undefined;
}

function fetchModels(kind: string): Promise<ModelOption[]> {
  const cacheable = !VOLATILE_MODEL_KINDS.has(kind);
  const ident = catalogIdent(kind);
  const hit = cacheable ? cachedOptions(kind) : undefined;
  if (hit) return Promise.resolve(hit);
  const flight = `${kind}|${ident}`;
  const hidden = localHidden(kind);
  // Whether the list that finally came back was shaped under this tab's hidden list; a list
  // that was not is still shown (the picker filters hidden ids itself) but never cached.
  let current = true;
  let p = inflight.get(flight);
  if (!p) {
    p = (async () => {
      for (let attempt = 0; ; attempt++) {
        const d = await requestModels(kind);
        // A list shaped under another hidden list (a change not yet saved) is asked again on
        // the same schedule, and used uncached once the attempts run out.
        if (d && (answeredUnder(d, hidden) || attempt >= MODELS_RETRY_MS.length)) {
          current = answeredUnder(d, hidden);
          return d;
        }
        if (attempt >= MODELS_RETRY_MS.length) {
          // Out of attempts. "Could not reach the Agent" is its own answer — the picker
          // must not fall back to naming the account's plan for it.
          emptyReasons.set(kind, "unreachable");
          throw new Error("empty");
        }
        await sleep(MODELS_RETRY_MS[attempt]);
      }
    })()
      .then((d: any) => {
        const items: {
          id?: string;
          label?: string;
          provider?: unknown;
          efforts?: unknown;
          defaultEffort?: unknown;
        }[] = Array.isArray(d?.models) ? d.models : [];
        const desc = items
          .filter((m) => m && typeof m.id === "string" && m.id)
          .map((m): ModelDescriptor => ({
            id: m.id!,
            label: m.label || m.id!,
            provider: typeof m.provider === "string" ? m.provider : "",
            efforts: Array.isArray(m.efforts) ? m.efforts.filter((x): x is string => typeof x === "string" && !!x) : [],
            defaultEffort: typeof m.defaultEffort === "string" ? m.defaultEffort : "",
          }));
        if (kind === "opencode") opencodeRoute = typeof d?.route === "string" ? d.route : "";
        const opts = desc.map((m): ModelOption => [m.id, decorateLabel(kind, m.label, desc)]);
        if (!opts.length) {
          // The Agent answered, and the answer was "none" — a different fact from a fetch
          // that never landed, and the only path on which it says why.
          emptyReasons.set(kind, typeof d?.reason === "string" ? d.reason : "catalog_empty");
          throw new Error("empty"); // workspace stopped / CLI absent — retry next open
        }
        emptyReasons.delete(kind);
        const full = [...defaultOnly(kind), ...opts];
        descriptors.set(kind, desc);
        if (cacheable && current) cache.set(kind, { ident, opts: full });
        else inflight.delete(flight);
        return full;
      })
      .catch((e) => {
        inflight.delete(flight);
        if (kind === "opencode") opencodeRoute = "";
        // A fetch that did not land tells us nothing about why the menu is empty, so the
        // previous answer must not be left standing as an explanation of this one. The
        // "empty" throw above is not that case — it IS the answer, reason and all.
        if (!(e instanceof Error && e.message === "empty")) emptyReasons.delete(kind);
        return defaultOnly(kind);
      });
    inflight.set(flight, p);
  }
  return p;
}

/** What "recommended" resolves to on one kind, per tier — the Agent's own answer (the
 *  `recommended` member of GET /agents/{kind}/models, chatx.RecommendedModels; Issue #972). ""
 *  means the Agent passes no model, so the CLI's own default runs. */
export interface RecommendedModels {
  chat: string;
  prose: string;
  short: string;
}

function parseRecommended(v: unknown): RecommendedModels | null {
  if (!v || typeof v !== "object") return null;
  const r = v as Record<string, unknown>;
  const str = (x: unknown) => (typeof x === "string" ? x : "");
  return { chat: str(r.chat), prose: str(r.prose), short: str(r.short) };
}

// Keyed by tenant, user, kind AND the kind's hidden-models list: hiding the recommended model
// moves the Agent's answer (to the next cheapest, or to the CLI default), so an answer fetched
// under another list is not this list's answer — and the Console switches tenant (and user)
// within one page load, where another tenant's Agent answers from another catalog (#972
// review, round 3). Kept apart from fetchModels because claude has no
// live catalog to fetch (CLAUDE_MODELS) yet still has a recommendation to ask for.
//
// An answer is kept for RECOMMENDED_TTL_MS only: the Agent's own answer moves without any
// Console action — a cheaper model ships, the daily price catalog changes, a model is retired —
// and a Console left open for days would otherwise keep naming the old one (#972 review).
const RECOMMENDED_TTL_MS = 10 * 60 * 1000;
const recommendedCache = new Map<string, { rec: RecommendedModels; at: number }>();
const recommendedInflight = new Map<string, Promise<RecommendedModels | null>>();

/** Forgets every fetched recommendation. For dom tests: they mount the same kind under the same
 *  hidden list case after case, so a module-scope answer from one case would be read back by
 *  the next (memory: module-scope-cache-leaks-across-dom-tests). */
export function clearRecommendedModels(): void {
  recommendedCache.clear();
  recommendedInflight.clear();
}

function cachedRecommended(key: string): RecommendedModels | null {
  const hit = recommendedCache.get(key);
  return hit && Date.now() - hit.at < RECOMMENDED_TTL_MS ? hit.rec : null;
}

// fetchRecommended retries on the same schedule as fetchModels: right after a workspace starts
// the Agent is not listening yet and the CP answers 502, and giving up on the first one left
// the label at a plain "推奨" until the settings were reopened (#972 review). A failure caches
// nothing.
function fetchRecommended(kind: string, key: string): Promise<RecommendedModels | null> {
  const hit = cachedRecommended(key);
  if (hit) return Promise.resolve(hit);
  let p = recommendedInflight.get(key);
  if (!p) {
    const hidden = localHidden(kind);
    p = (async () => {
      for (let attempt = 0; ; attempt++) {
        const d = await requestModels(kind);
        const r = parseRecommended(d?.recommended);
        // Kept only when computed under the hidden list this key stands for (answeredUnder);
        // otherwise asked again, and given up on — naming no model — once the attempts run out.
        if (r && answeredUnder(d, hidden)) {
          recommendedCache.set(key, { rec: r, at: Date.now() });
          return r;
        }
        if (attempt >= MODELS_RETRY_MS.length) return null;
        await sleep(MODELS_RETRY_MS[attempt]);
      }
    })().finally(() => recommendedInflight.delete(key));
    recommendedInflight.set(key, p);
  }
  return p;
}

// useRecommendedModels is the Agent's "recommended" for `kind` — null until it has answered
// (or when it could not be reached), in which case a caller names no model rather than guess
// one: the Console used to re-derive this itself (aiModelRow.tsx's recommendedModelId) and the
// two drifted.
export function useRecommendedModels(kind: string): RecommendedModels | null {
  const hiddenModels = useSettings().hiddenModels;
  const key = [getTenant(), getUser(), kind, JSON.stringify(hiddenModelsFor(hiddenModels, kind))].join("|");
  // The answer is held WITH the key it answers, and only returned for the current key: a
  // different tenant, kind or hidden list is a different question, and even the one render
  // between the key changing and an effect clearing the state must not show the old answer.
  const [held, setHeld] = useState<{ key: string; rec: RecommendedModels } | null>(() => {
    const rec = cachedRecommended(key);
    return rec ? { key, rec } : null;
  });
  // round advances every RECOMMENDED_TTL_MS while mounted: a settings tab left open would
  // otherwise keep the answer it opened with, however old (#972 review, round 2).
  const [round, setRound] = useState(0);
  useEffect(() => {
    let alive = true;
    // A refresh round keeps showing the previous answer until the new one lands, and keeps it
    // if the Agent cannot be reached — an older answer beats a label that names nothing.
    void fetchRecommended(kind, key).then((r) => alive && r && setHeld({ key, rec: r }));
    const timer = setTimeout(() => setRound((n) => n + 1), RECOMMENDED_TTL_MS);
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [kind, key, round]);
  if (held?.key === key) return held.rec;
  return cachedRecommended(key);
}

// modelProviderOf answers which company made a model, for the picker's brand mark. Read
// straight off the fetched descriptors rather than through state: fetchModels fills them
// before it resolves the options, so any render that can see a model in the list can see its
// provider too (the same arrangement useEffortOptions already relies on).
//
// "" means "say nothing" — a kind with no live catalog (claude), the Default entry, a model
// rescued back into the list from a stored setting, or one the Agent could not place. The
// caller must draw no mark then; there is no house default, because a wrong logo beside a
// model someone is about to pay for is worse than a plain row.
export function modelProviderOf(kind: string, id: string): string {
  if (!id) return "";
  return descriptors.get(kind)?.find((m) => m.id === id)?.provider || "";
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
  // Held with the identity it was fetched under, so the render right after a tenant / hidden /
  // route change shows no list from before it (#972 review, round 4).
  const [held, setHeld] = useState<{ ident: string; opts: ModelOption[] } | null>(null);
  // The list is SHAPED server-side by settings (opencode's billing route, hidden models) and
  // belongs to one tenant and user, so any change to those refetches (catalogIdent) — otherwise
  // the picker keeps showing the old list until the Console is reloaded.
  const s = useSettings();
  const ident = catalogIdent(kind);
  useEffect(() => {
    if (!isDynamic(kind)) return;
    let alive = true;
    void fetchModels(kind).then((l) => alive && setHeld({ ident, opts: l }));
    return () => {
      alive = false;
    };
  }, [kind, ident]);
  const opts = (held?.ident === ident ? held.opts : undefined) || cachedOptions(kind) || defaultOnly(kind);
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

// useAutoConcreteModel is the launch dialogs' auto-pick guard for a kind with no CLI-picked
// own default (requiresConcreteModel — today: lcpp): once the live catalog settles with at
// least one entry AND the caller's own model state is still "", it fires onChange with the
// first entry, so a launch dialog never sits there offering an empty selection that can never
// actually be sent (docs/log/109). A no-op for every other kind, and a no-op once `model` is
// non-empty (a stored per-repo/global default, or an earlier auto-pick) — never overrides a
// deliberate choice.
export function useAutoConcreteModel(kind: string, model: string, onChange: (model: string) => void): void {
  const options = useModelOptions(kind);
  useEffect(() => {
    if (!requiresConcreteModel(kind) || model) return;
    const first = options?.find(([id]) => id)?.[0];
    if (first) onChange(first);
  }, [kind, model, options, onChange]);
}

// resolveQuickLaunchModel is quick launch's (RepoRowConnected's ▼ / right-click, which has no
// mounted picker to react to a catalog fetch) counterpart to useAutoConcreteModel: when
// `resolved` (repoLast.ts's resolveModel chain) came back empty for a kind that requires a
// concrete model, await the live catalog once and return its first entry. Returns `resolved`
// unchanged for every other kind, and "" (never a fabricated id) when the catalog itself turns
// out empty — the caller's own POST then reaches the server's create-time guard, which is the
// authoritative refusal for that case (docs/log/109).
export async function resolveQuickLaunchModel(kind: string, resolved: string): Promise<string> {
  if (resolved || !requiresConcreteModel(kind)) return resolved;
  const options = await fetchModels(kind);
  return options.find(([id]) => id)?.[0] || "";
}

// useOpencodeAppliedRoute returns the billing route the Agent actually shaped the launch list
// with, for the settings card to compare against the selected one. "" until a list has been
// fetched (and after a failure), which reads as "nothing to say".
//
// It goes through the same fetchModels as the picker, so opening Settings costs no extra
// request while a launch modal is open — opencode's entry is deliberately uncached but folds
// through `inflight`.
export function useOpencodeAppliedRoute(): string {
  const selected = useSettings().opencodeCatalog;
  const [route, setRoute] = useState(opencodeRoute);
  useEffect(() => {
    let alive = true;
    void fetchModels("opencode").then(() => alive && setRoute(opencodeRoute));
    return () => {
      alive = false;
    };
  }, [selected]); // a route change reshapes the list server-side, so the answer can change
  return route;
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
  // Settled for the identity the list is fetched under (catalogIdent), not for the kind alone:
  // after a tenant / hidden / route change the new list is in flight, and the note must wait.
  const ident = catalogIdent(kind);
  const [settledFor, setSettledFor] = useState<string | null>(null);
  useEffect(() => {
    if (!isDynamic(kind)) return;
    let alive = true;
    void fetchModels(kind).then(() => alive && setSettledFor(ident));
    return () => {
      alive = false;
    };
  }, [kind, ident]);
  return !isDynamic(kind) || settledFor === ident || cachedOptions(kind) !== undefined;
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
