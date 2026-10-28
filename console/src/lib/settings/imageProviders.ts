// Image-generation provider ordering (ADR 0082): the static fallback table and the
// normalisation the Agent applies to imageProviderOrder, so the list a member drags is the list
// "auto" walks. Re-exported from lib/settings.ts.

// 🔴 STATIC FALLBACK ONLY (ADR 0082 decision 3). The list this deployment's fleet actually
// serves is no longer knowable statically — since ADR 0082 P0 a fleet provider's id is the
// engine table's own free-text KEY ("image", "comfy-lan", …), an operator's choice this bundle
// cannot predict — so the real answer comes from the Agent (`GET /imagegen/status`, the same
// `providers[]`/`fleet` the image generation pane already reads; see settingsImageRows.ts).
//
// This table is read only when that answer is not available: the Workspace is stopped or still
// loading, or an old Agent build that predates the `fleet` field. `agy` and `codex` are NOT
// fallback-only — they are vendor routes, never rows, and are offered for ranking regardless of
// whether the member is currently logged into either (see normalizeImageProviderOrder).
export const IMAGE_PROVIDERS_RANKED = [
  { id: "openai-compat", fleet: true },
  { id: "comfy", fleet: true },
  { id: "agy", fleet: false },
  { id: "codex", fleet: false },
] as const;

// `fleet` above is the ONE fact that decides where a provider the stored order never named gets
// inserted (see normalizeImageProviderOrder), and it is declared here rather than tested for by
// id wherever it matters — an `id === "comfy"` in a condition is how the next provider gets
// forgotten. It mirrors providerRanks in the Agent's internal/imagegen/imagegen.go; the two lists
// have to agree, or the order the user drags is not the order "auto" walks — this is the fallback
// half only; the live half is settingsImageRows' `fleet` bit, straight off the wire.
export const IMAGE_PROVIDERS: readonly string[] = IMAGE_PROVIDERS_RANKED.map((p) => p.id);

// imageProviderIsFleet — whose hardware serves this route, IN THE FALLBACK TABLE. Unknown ids
// are not the fleet's: an id nobody declared is not something this deployment can be said to pay
// for. Only used by the fallback path (see normalizeImageProviderOrder's `rows` parameter for the
// live one) and by the folded display (collapseImageProviderOrder), which is fallback-only too.
export function imageProviderIsFleet(id: string): boolean {
  return IMAGE_PROVIDERS_RANKED.some((p) => p.id === id && p.fleet);
}

// imageProviderLabel names one row of the FALLBACK ordering list (see IMAGE_PROVIDERS_RANKED).
// agy and codex are agent kinds and carry their own display name; the fleet's own engine is not
// an agent at all — it is a service this deployment runs — so it has its own label rather than a
// lookup that would return the id.
//
// This is the fallback's label only. Once the Agent has answered (ADR 0082 decision 3), each row
// draws its OWN key plus a short kind label built from `settingsImageRows.ts`'s `kind` — see
// AgentsTab, which is also where the two paths (live vs fallback) are chosen between.
export function imageProviderLabel(id: string): string {
  if (id === IMAGE_PROVIDER_FLEET_GROUP || imageProviderIsFleet(id)) return "Agent Fleet (self-hosted)";
  return "";
}

// IMAGE_PROVIDER_FLEET_GROUP is the ONE row the fleet's own providers share in the FALLBACK
// ordering list only (ADR 0082 decision 8). Once the Agent has answered, the list is drawn one
// row per DECLARED row instead (AgentsTab), and this fold does not apply to it at all — decision
// 8 says so outright: "リストが行ごとになった時点でこの畳み込みは消える".
//
// The fallback still needs it: with no live answer there is no way to know which of
// `openai-compat` / `comfy` (if either) this deployment actually runs, or under what key, so
// showing two separately-rankable placeholders would be a rank a member can drag with nothing
// real behind it (ADR 0083 decision 8's original reasoning, now scoped to the fallback path).
// Collapsing them into one row is a DISPLAY change only: the stored setting keeps both ids,
// because a stored order that dropped one would push it last on the day a deployment adds a row
// for it, which is what normalizeImageProviderOrder's fallback branch exists to prevent.
//
// The `@` prefix is not decoration: it keeps this pseudo id outside the space of real provider
// ids (ADR 0082 makes those the engine table's keys, which are free text).
export const IMAGE_PROVIDER_FLEET_GROUP = "@fleet";

// collapseImageProviderOrder turns a stored order into the FALLBACK rows to draw: the first
// fleet id becomes the group, any further one disappears into it. Takes an already-normalised
// order. Fallback-only — see IMAGE_PROVIDER_FLEET_GROUP.
export function collapseImageProviderOrder(order: string[]): string[] {
  const out: string[] = [];
  for (const id of order) {
    if (!imageProviderIsFleet(id)) {
      out.push(id);
      continue;
    }
    if (!out.includes(IMAGE_PROVIDER_FLEET_GROUP)) out.push(IMAGE_PROVIDER_FLEET_GROUP);
  }
  return out;
}

// expandImageProviderOrder is the way back, for what the member just dragged in the FALLBACK
// view: the group becomes every fallback fleet id, in the built-in order, at the rank the group
// now holds. Fallback-only — see IMAGE_PROVIDER_FLEET_GROUP.
//
// So two fleet ids that were stored apart come back adjacent. That is the honest reading of a
// list where they were one row, and it changes nothing that runs: only one of them is ever
// served, and the other is skipped exactly like an unusable login.
export function expandImageProviderOrder(display: string[]): string[] {
  const fleet = IMAGE_PROVIDERS_RANKED.filter((p) => p.fleet).map((p) => p.id);
  const out: string[] = [];
  for (const id of display) {
    if (id === IMAGE_PROVIDER_FLEET_GROUP) {
      out.push(...fleet.filter((f) => !out.includes(f)));
      continue;
    }
    if (!out.includes(id)) out.push(id);
  }
  return normalizeImageProviderOrder(out);
}

// ImageFleetRow is one images-API row as the ordering logic needs it (ADR 0082 decision 1),
// straight off `/imagegen/status`'s `providers[]` filtered to `fleet`: the row's own KEY (its
// id everywhere else) and its declared KIND (`comfy` / `openai-compat`), needed only to expand a
// legacy stored alias — see normalizeImageProviderOrder. It never decides fleet-ness itself:
// every entry in this list already IS a fleet row by construction (ADR 0082 decision 4).
export interface ImageFleetRow {
  id: string;
  kind: string;
}

// normalizeImageProviderOrder folds any stored value into a total order — the same rules the
// Agent applies in imagegen.effectiveOrder() / normalizeStoredProviderOrder(), so the list the
// user drags is exactly the list "auto" will walk.
//
// `rows`, when given, is the Agent's own live answer (ADR 0082 decision 3): every images row
// this deployment currently declares, REPLACING the two static fallback placeholders
// (`openai-compat`, `comfy`) as the fleet half of the universe this function folds over. `agy`
// and `codex` are always the static two — they are vendor routes, never rows, and stay
// rankable whether or not the member is logged into either right now.
//
// omitted `rows` (undefined, not an empty array) means "no live answer yet" (Workspace stopped,
// still loading, or an Agent too old to send `fleet`) and falls back to IMAGE_PROVIDERS exactly
// as before this function grew the parameter. An empty array is a real answer — "this deployment
// declares zero images rows" — and must NOT fall back to the two placeholders: that would offer
// two rows nothing can ever route to, which the live path exists to stop doing (ADR 0082
// decision 8, "行ごとになった時点でこの畳み込みは消える" applies here too — a deployment with
// nothing to serve shows nothing).
//
// 🔴 A provider the stored value never named goes to the FRONT when the fleet serves it and to
// the back when it does not. Appending everything was measured doing real harm (ADR 0072's
// 2026-09-11 hardware follow-up): a stored `["sdcpp","agy","codex"]`, written before `comfy`
// existed, pushed the fleet's own GPU behind two personal plans, so a call naming no provider
// spent a member's quota on hardware the deployment was already paying for. What the stored value
// DOES name keeps its relative order — including a fleet provider the user deliberately ranked
// last. This is the property ADR 0082 decision 3 requires survive the move to live rows, and it
// does: an unmentioned id is still classified fleet-or-not off the SAME universe this function
// folds `chosen` from, live or fallback.
//
// 🔥 A stored `comfy` / `openai-compat` (the only ids a preference saved before ADR 0082 could
// ever have named) is a LEGACY KIND ALIAS once `rows` is live, not a rank of its own: it expands
// to every row `rows` currently declares of that kind, in the order `rows` lists them, mirroring
// the Agent's normalizeStoredProviderOrder exactly. Leaving it un-expanded would let a row whose
// key is not literally "comfy" fall out of a stored order the member already customised.
export function normalizeImageProviderOrder(v: unknown, rows?: ImageFleetRow[]): string[] {
  const live = rows !== undefined;
  const known: readonly string[] = live ? [...rows.map((r) => r.id), "agy", "codex"] : IMAGE_PROVIDERS;
  const isFleet = (id: string): boolean => (live ? rows.some((r) => r.id === id) : imageProviderIsFleet(id));
  const byKind = new Map<string, string[]>();
  if (live) for (const r of rows) byKind.set(r.kind, [...(byKind.get(r.kind) || []), r.id]);

  const chosen: string[] = [];
  const seen = new Set<string>();
  const push = (k: unknown) => {
    if (typeof k === "string" && known.includes(k) && !seen.has(k)) {
      seen.add(k);
      chosen.push(k);
    }
  };
  const raw = Array.isArray(v) ? v : [];
  for (const k of raw) {
    if (live && typeof k === "string" && byKind.has(k)) {
      byKind.get(k)!.forEach(push);
      continue;
    }
    push(k);
  }
  const rest = known.filter((id) => !seen.has(id));
  return [...rest.filter(isFleet), ...chosen, ...rest.filter((id) => !isFleet(id))];
}
