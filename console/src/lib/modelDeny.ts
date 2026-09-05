// Models the user has excluded (Settings > Agents > each card > behaviour, settings.hiddenModels).
// The matching rule is paired with the Agent's workspace/agent/model_deny.go and both sides must
// change together: if they drift, a model shows up in the picker but launching it is refused with
// a 400.
//
// The rule: fold the separators (/ . _ space :) to hyphens, lowercase, then accept an exact match
// plus — only when the hidden entry is a single token — a containment match on token boundaries.
//   - "fable" also matches "claude-fable-5" (claude accepts either the alias or the full id as
//     --model)
//   - "gpt-5.4" (multiple tokens, i.e. a concrete id) does not match "gpt-5.4-mini"
//   - "opencode/glm-5.2" does not match "opencode-go/glm-5.2", so hiding one billing route does
//     not take out the other
//   - a mere substring such as "fablet" does not match

export function normModelToken(s: string): string {
  let out = (s || "").trim().toLocaleLowerCase().replace(/[/._ :]/g, "-");
  while (out.includes("--")) out = out.replace(/--/g, "-");
  return out.replace(/^-+|-+$/g, "");
}

export function modelMatchesHidden(requested: string, hidden: string): boolean {
  const r = normModelToken(requested);
  const h = normModelToken(hidden);
  if (!r || !h) return false;
  if (r === h) return true;
  if (h.includes("-")) return false; // a concrete id was hidden; another model sharing its prefix is a different model
  return `-${r}-`.includes(`-${h}-`);
}

// hiddenModelsFor is the effective exclusion list for a kind. claude alone has a fail-safe: its
// four fixed tiers offer no "default" option, so hiding all of them would leave no launchable
// model. Same rule as the Agent side.
export function hiddenModelsFor(
  hiddenModels: Record<string, string[]> | undefined,
  kind: string,
  catalogIds?: string[],
): string[] {
  const raw = hiddenModels?.[kind];
  if (!Array.isArray(raw)) return [];
  const list = raw.filter((v): v is string => typeof v === "string" && !!v.trim());
  if (!list.length) return [];
  if (catalogIds?.length && catalogIds.every((id) => list.some((h) => modelMatchesHidden(id, h)))) {
    return []; // a config that hides everything is ignored
  }
  return list;
}

export function isModelHidden(
  hiddenModels: Record<string, string[]> | undefined,
  kind: string,
  model: string,
  catalogIds?: string[],
): boolean {
  if (!model.trim()) return false; // unset means "leave it to the CLI default"
  return hiddenModelsFor(hiddenModels, kind, catalogIds).some((h) => modelMatchesHidden(model, h));
}
