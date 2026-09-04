// Data layer for settings export / import (docs/log/79 / ADR 0060).
//
// A single JSON file (the bundle) carries only the layers of a person's settings that hold
// no secrets:
//   prefs        - Console personal settings (exactly what ui-prefs syncs)
//   ssm          - AWS SSM profiles / hosts (CP database, per member)
//   instructions - user instructions (~/.config/agent-fleet/user-notes.md)
// Connections (Git / agent / AWS tokens) are never included. The bundle is plain text meant
// to travel by mail or chat, so a single secret in it would change how the whole file must
// be handled.
//
// Pure logic only: no fetch, no React imports. The settings defaults and the "is this key
// accumulated" predicate come from the caller (settings.ts), because settings.ts touches
// localStorage and cannot be imported from node tests — mixing them would turn every test
// here into a DOM test.
//
// Two design points:
//   1. Hosts reference a profile by its display name, not by id. CP ids differ per
//      environment, so carrying an id always forces a re-link on import. The display name is
//      also the basis of the ~/.aws profile name and is a natural key a human can read, so
//      the format itself removes the id problem.
//   2. Import only adds. Anything already present is left alone (same-named profile, host
//      with the same alias+instance). Deleting from an existing environment to move settings
//      in is not worth the risk.

export const BUNDLE_KIND = "agent-fleet-settings";
export const BUNDLE_VERSION = 1;

export type SectionKey = "prefs" | "ssm" | "instructions";
export const SECTION_KEYS: SectionKey[] = ["prefs", "ssm", "instructions"];

export interface SsmProfileEntry {
  label: string;
  startUrl: string;
  ssoRegion: string;
  accountId: string;
  roleName: string;
  region: string;
}

export interface SsmHostEntry {
  alias: string;
  /** Display name of the referenced profile, not its id (design point 2 above). */
  profile: string;
  instanceId: string;
  documentName: string;
  region: string;
}

export interface SsmSection {
  profiles: SsmProfileEntry[];
  hosts: SsmHostEntry[];
}

export interface InstructionsSection {
  text: string;
  enabled: boolean;
  targets: Record<string, boolean>;
}

export interface BundleSections {
  prefs?: Record<string, unknown>;
  ssm?: SsmSection;
  instructions?: InstructionsSection;
}

export interface SettingsBundle {
  kind: string;
  version: number;
  exportedAt: string;
  sections: BundleSections;
}

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");
const key = (v: string): string => v.trim().toLowerCase();

// --- Export ---------------------------------------------------------------------

/** Shallow copy of the settings holding only known keys. Unknown keys written by an older
 *  Console are dropped: the import side rejects them anyway, so carrying them is pointless. */
export function exportablePrefs(
  state: Record<string, unknown>,
  defaults: Record<string, unknown>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const k of Object.keys(defaults)) {
    if (k in state) out[k] = state[k];
  }
  return out;
}

/** Convert the CP DTOs (id references) into the bundle shape (display-name references). A
 *  host whose profile cannot be resolved keeps an empty profile, so the import side skips it
 *  with a reason. */
export function toSsmSection(profiles: any[], hosts: any[]): SsmSection {
  const labelOf = new Map<string, string>();
  for (const p of profiles || []) labelOf.set(String(p?.id ?? ""), str(p?.label));
  return {
    profiles: (profiles || []).map((p) => ({
      label: str(p?.label),
      startUrl: str(p?.startUrl),
      ssoRegion: str(p?.ssoRegion),
      accountId: str(p?.accountId),
      roleName: str(p?.roleName),
      region: str(p?.region),
    })),
    hosts: (hosts || []).map((h) => ({
      alias: str(h?.alias),
      profile: labelOf.get(String(h?.profileId ?? "")) ?? "",
      instanceId: str(h?.instanceId),
      documentName: str(h?.documentName),
      region: str(h?.region),
    })),
  };
}

/** Convert the user-notes GET response (targets is an array) into the bundle shape
 *  (kind -> on/off). */
export function toInstructionsSection(payload: any): InstructionsSection {
  const targets: Record<string, boolean> = {};
  for (const t of payload?.targets || []) {
    if (t && typeof t.kind === "string" && t.supported) targets[t.kind] = t.on === true;
  }
  return {
    text: typeof payload?.text === "string" ? payload.text : "",
    enabled: payload?.enabled !== false,
    targets,
  };
}

export function buildBundle(sections: BundleSections, exportedAt: string): SettingsBundle {
  return { kind: BUNDLE_KIND, version: BUNDLE_VERSION, exportedAt, sections };
}

/** Export file name (af-settings-YYYYMMDD-HHmm.json). The time is local. */
export function bundleFileName(at: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return (
    "af-settings-" +
    at.getFullYear() +
    p(at.getMonth() + 1) +
    p(at.getDate()) +
    "-" +
    p(at.getHours()) +
    p(at.getMinutes()) +
    ".json"
  );
}

// --- Parsing --------------------------------------------------------------------

export type ParseError = "bad_json" | "bad_kind" | "bad_version" | "empty";

/** Read the received JSON as a bundle. Failures come back as a reason code; the caller
 *  supplies the wording. */
export function parseBundle(text: string): { bundle: SettingsBundle } | { error: ParseError } {
  let raw: any;
  try {
    raw = JSON.parse(text);
  } catch {
    return { error: "bad_json" };
  }
  if (!raw || typeof raw !== "object" || raw.kind !== BUNDLE_KIND) return { error: "bad_kind" };
  // The version is deliberately not forward-compatible. Partially applying an unknown
  // version leaves the user with no way to tell what was imported and what was not.
  if (raw.version !== BUNDLE_VERSION) return { error: "bad_version" };
  const src = raw.sections && typeof raw.sections === "object" ? raw.sections : {};
  const sections: BundleSections = {};
  if (src.prefs && typeof src.prefs === "object" && !Array.isArray(src.prefs)) {
    sections.prefs = src.prefs as Record<string, unknown>;
  }
  if (src.ssm && typeof src.ssm === "object") {
    sections.ssm = {
      profiles: Array.isArray(src.ssm.profiles) ? src.ssm.profiles : [],
      hosts: Array.isArray(src.ssm.hosts) ? src.ssm.hosts : [],
    };
  }
  if (src.instructions && typeof src.instructions === "object") {
    const t = src.instructions.targets;
    sections.instructions = {
      text: typeof src.instructions.text === "string" ? src.instructions.text : "",
      enabled: src.instructions.enabled !== false,
      targets: t && typeof t === "object" && !Array.isArray(t) ? (t as Record<string, boolean>) : {},
    };
  }
  if (!sections.prefs && !sections.ssm && !sections.instructions) return { error: "empty" };
  return { bundle: { kind: raw.kind, version: raw.version, exportedAt: str(raw.exportedAt), sections } };
}

// --- Import (personal settings) -------------------------------------------------

/** Keep only known keys whose value shape matches the default. A mismatched value (another
 *  Console version, or a hand edit) put straight into state breaks every reader at once. */
export function sanitizeImportedPrefs(
  raw: Record<string, unknown>,
  defaults: Record<string, unknown>,
): { patch: Record<string, unknown>; skipped: string[] } {
  const patch: Record<string, unknown> = {};
  const skipped: string[] = [];
  for (const [k, v] of Object.entries(raw || {})) {
    if (!(k in defaults)) {
      skipped.push(k);
      continue;
    }
    if (!sameShape(defaults[k], v)) {
      skipped.push(k);
      continue;
    }
    patch[k] = v;
  }
  return { patch, skipped };
}

function sameShape(def: unknown, v: unknown): boolean {
  if (Array.isArray(def)) return Array.isArray(v);
  if (def === null) return true; // a null default pins no shape
  if (typeof def === "object") return !!v && typeof v === "object" && !Array.isArray(v);
  return typeof v === typeof def;
}

/** Layer the imported values onto the current ones. Accumulated data (learned suggestions,
 *  key bindings, working sets, ...) is never overwritten by an empty value, and objects are
 *  merged rather than replaced: the same hole that once wiped every device's accumulated data
 *  through a whole-object PUT (see the prefsLoaded comment in settings.ts) must not be
 *  reopened by import, which is a single irreversible action. Accumulated arrays (reply
 *  suggestions and the like) are replaced, because element identity cannot be judged here,
 *  but never by an empty array. */
export function mergeImportedPrefs(
  current: Record<string, unknown>,
  patch: Record<string, unknown>,
  isAccumulated: (key: string) => boolean,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(patch)) {
    if (!isAccumulated(k)) {
      out[k] = v;
      continue;
    }
    if (isEmptyValue(v)) continue; // never overwrite with an empty value
    const cur = current[k];
    if (isPlainObject(v) && isPlainObject(cur)) {
      out[k] = { ...(cur as object), ...(v as object) };
      continue;
    }
    out[k] = v;
  }
  return out;
}

function isPlainObject(v: unknown): boolean {
  return !!v && typeof v === "object" && !Array.isArray(v);
}

function isEmptyValue(v: unknown): boolean {
  if (v == null || v === "") return true;
  if (Array.isArray(v)) return v.length === 0;
  if (typeof v === "object") return Object.keys(v as object).length === 0;
  return false;
}

// --- Import (SSM) ---------------------------------------------------------------

export type SkipReason = "exists" | "invalid" | "no_profile";

export interface SsmPlan {
  /** Profiles to create. */
  profiles: SsmProfileEntry[];
  /** Hosts to create; profile is still a display name and is resolved to an id after the
   *  profiles have been created. */
  hosts: SsmHostEntry[];
  skippedProfiles: { label: string; reason: SkipReason }[];
  skippedHosts: { alias: string; reason: SkipReason }[];
}

/** Match the parsed section against what already exists and narrow it to what will actually
 *  be created. A profile counts as existing when the display name matches; a host when both
 *  alias and instance id match. */
export function planSsmImport(
  section: SsmSection,
  existingProfiles: any[],
  existingHosts: any[],
): SsmPlan {
  const plan: SsmPlan = { profiles: [], hosts: [], skippedProfiles: [], skippedHosts: [] };
  const haveProfile = new Set((existingProfiles || []).map((p) => key(str(p?.label))));
  const haveHost = new Set(
    (existingHosts || []).map((h) => key(str(h?.alias)) + "\u0000" + key(str(h?.instanceId))),
  );
  // Profile names referenceable after the import = existing plus the ones about to be made.
  const willHave = new Set(haveProfile);
  for (const raw of section.profiles || []) {
    const p: SsmProfileEntry = {
      label: str(raw?.label),
      startUrl: str(raw?.startUrl),
      ssoRegion: str(raw?.ssoRegion),
      accountId: str(raw?.accountId),
      roleName: str(raw?.roleName),
      region: str(raw?.region),
    };
    // Same minimum condition as CP's validateProfile. Without dropping these here the user
    // just sees a row of 400s and cannot tell how many entries were imported.
    if (!p.label || !/^https:\/\/\S+$/.test(p.startUrl) || !p.ssoRegion) {
      plan.skippedProfiles.push({ label: p.label, reason: "invalid" });
      continue;
    }
    if (willHave.has(key(p.label))) {
      plan.skippedProfiles.push({ label: p.label, reason: "exists" });
      continue;
    }
    willHave.add(key(p.label));
    plan.profiles.push(p);
  }
  const seenHost = new Set(haveHost);
  for (const raw of section.hosts || []) {
    const h: SsmHostEntry = {
      alias: str(raw?.alias),
      profile: str(raw?.profile),
      instanceId: str(raw?.instanceId),
      documentName: str(raw?.documentName),
      region: str(raw?.region),
    };
    if (!h.alias || !h.instanceId) {
      plan.skippedHosts.push({ alias: h.alias, reason: "invalid" });
      continue;
    }
    const id = key(h.alias) + "\u0000" + key(h.instanceId);
    if (seenHost.has(id)) {
      plan.skippedHosts.push({ alias: h.alias, reason: "exists" });
      continue;
    }
    if (!h.profile || !willHave.has(key(h.profile))) {
      plan.skippedHosts.push({ alias: h.alias, reason: "no_profile" });
      continue;
    }
    seenHost.add(id);
    plan.hosts.push(h);
  }
  return plan;
}

/** Display name -> CP id table, built from the list of profiles that now exist. */
export function profileIdByLabel(profiles: any[]): Map<string, string> {
  const m = new Map<string, string>();
  for (const p of profiles || []) {
    const label = key(str(p?.label));
    if (label && !m.has(label)) m.set(label, String(p?.id ?? ""));
  }
  return m;
}

// --- Summary --------------------------------------------------------------------

export interface BundleSummary {
  prefs: number;
  profiles: number;
  hosts: number;
  instructionBytes: number;
  instructions: boolean;
}

export function summarizeBundle(b: SettingsBundle): BundleSummary {
  const s = b.sections;
  return {
    prefs: s.prefs ? Object.keys(s.prefs).length : 0,
    profiles: s.ssm?.profiles.length ?? 0,
    hosts: s.ssm?.hosts.length ?? 0,
    instructionBytes: s.instructions ? utf8Bytes(s.instructions.text) : 0,
    instructions: !!s.instructions,
  };
}

export function utf8Bytes(s: string): number {
  if (typeof TextEncoder !== "undefined") return new TextEncoder().encode(s).byteLength;
  return s.length;
}
