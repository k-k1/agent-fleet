// Display-name/description resolution for assistants (docs/log/28 P3). Builtin assistants'
// user-facing name/description live in the Console i18n catalog, keyed by their stable
// id (assistant.<id>.name / .desc) — the Agent still returns Japanese values as a
// fallback for older fleets that predate the catalog. User-defined assistants have no
// catalog key, so tMaybe returns undefined and we fall back to the stored value.
//
// These read the current locale via tMaybe at call time, so callers must be subscribed
// to locale changes (useT) to re-render on a language switch.
import type { Assistant } from "../../types/assistant.ts";
import { tMaybe } from "../../lib/i18n/index.ts";
import type { ApiError } from "../../core/api/client.ts";

export const assistantName = (a: Assistant): string =>
  (a.builtin ? tMaybe("assistant." + a.id + ".name") : undefined) ?? a.name;

export const assistantDesc = (a: Assistant): string | undefined =>
  (a.builtin ? tMaybe("assistant." + a.id + ".desc") : undefined) ?? a.description;

// assistantSaveError is the toast for a refused create/update. Only the Agent's assistant_*
// codes are localized reasons; anything else (a 5xx, a disk error, no response) keeps
// `fallback` rather than surfacing a developer message. integration_unsupported carries the
// rejected id in its own field, appended after the localized text.
export const assistantSaveError = (
  err: (ApiError & { integration?: string }) | null | undefined,
  fallback: string,
): string => {
  const why = err?.code?.startsWith("assistant_") ? tMaybe("err." + err.code) : undefined;
  if (!why) return fallback;
  return err?.integration ? why + ": " + err.integration : why;
};
