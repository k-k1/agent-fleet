// Data layer for settings export / import (docs/log/79 / ADR 0060).
//
// A single JSON file (the bundle) carries only the layers of a person's settings that hold
// no secrets:
//   prefs        - Console personal settings (exactly what ui-prefs syncs)
//   ssm          - AWS SSM profiles / hosts (CP database, per member)
//   gcpProfiles  - Google Cloud profiles (CP database, per member; ADR 0107)
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
import { validDuration, validExternalId, validRoleArn, validSessionName } from "./awsRoleChain.ts";

export const BUNDLE_VERSION = 1;

export type SectionKey = "prefs" | "ssm" | "gcpProfiles" | "instructions";
export const SECTION_KEYS: SectionKey[] = ["prefs", "ssm", "gcpProfiles", "instructions"];

export interface SsmProfileEntry {
  label: string;
  startUrl: string;
  ssoRegion: string;
  accountId: string;
  roleName: string;
  region: string;
  /** Only on an assume-role profile (issue #1109); an sso entry carries none of these, so its
   *  bytes are what they always were. `source` is the display name of the SSO profile it
   *  assumes from, not its id (the destination assigns new ids). */
  kind?: "assume_role";
  source?: string;
  /** The source's SSO coordinates (ssoKey), so an import joins the chain to the same sign-in
   *  and never to another profile that happens to share the label. */
  sourceKey?: string;
  roleArn?: string;
  externalId?: string;
  sessionName?: string;
  durationSeconds?: number;
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

/** A Google Cloud profile as the bundle carries it: the row without its id and its
 *  CP-computed name, both of which the destination assigns again. */
export interface GcpProfileEntry {
  label: string;
  loginMethod: string;
  project: string;
  quotaProject: string;
  account: string;
  region: string;
  zone: string;
  impersonateServiceAccount: string;
}

export interface InstructionsSection {
  text: string;
  enabled: boolean;
  targets: Record<string, boolean>;
}

export interface BundleSections {
  prefs?: Record<string, unknown>;
  ssm?: SsmSection;
  gcpProfiles?: GcpProfileEntry[];
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
/** Canonical identity of an SSO profile's sign-in: portal, SSO region, account and role. */
export function ssoKey(p: any): string {
  return [str(p?.startUrl).replace(/\/+$/, ""), str(p?.ssoRegion), str(p?.accountId), str(p?.roleName)].join("|");
}

export function toSsmSection(profiles: any[], hosts: any[]): SsmSection {
  const labelOf = new Map<string, string>();
  const byId = new Map<string, any>();
  for (const p of profiles || []) {
    labelOf.set(String(p?.id ?? ""), str(p?.label));
    byId.set(String(p?.id ?? ""), p);
  }
  return {
    profiles: (profiles || []).map((p) =>
      p?.kind === "assume_role"
        ? {
            kind: "assume_role" as const,
            label: str(p?.label),
            startUrl: "",
            ssoRegion: "",
            accountId: str(p?.accountId),
            roleName: "",
            region: str(p?.region),
            source: labelOf.get(String(p?.sourceProfileId ?? "")) ?? "",
            sourceKey: byId.has(String(p?.sourceProfileId ?? "")) ? ssoKey(byId.get(String(p.sourceProfileId))) : "",
            roleArn: str(p?.roleArn),
            externalId: str(p?.externalId),
            sessionName: str(p?.sessionName),
            ...(Number(p?.durationSeconds) > 0 ? { durationSeconds: Number(p.durationSeconds) } : {}),
          }
        : {
            label: str(p?.label),
            startUrl: str(p?.startUrl),
            ssoRegion: str(p?.ssoRegion),
            accountId: str(p?.accountId),
            roleName: str(p?.roleName),
            region: str(p?.region),
          },
    ),
    hosts: (hosts || []).map((h) => ({
      alias: str(h?.alias),
      profile: labelOf.get(String(h?.profileId ?? "")) ?? "",
      instanceId: str(h?.instanceId),
      documentName: str(h?.documentName),
      region: str(h?.region),
    })),
  };
}

function gcpEntry(raw: any): GcpProfileEntry {
  return {
    label: str(raw?.label),
    loginMethod: str(raw?.loginMethod) || "google",
    project: str(raw?.project),
    quotaProject: str(raw?.quotaProject),
    account: str(raw?.account),
    region: str(raw?.region),
    zone: str(raw?.zone),
    impersonateServiceAccount: str(raw?.impersonateServiceAccount),
  };
}

/** Convert the CP's Google Cloud rows into the bundle shape: only the fields a person
 *  entered, so `id`, `name` and `conflict` never travel. */
export function toGcpSection(profiles: any[]): GcpProfileEntry[] {
  return (profiles || []).map(gcpEntry);
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
  if (Array.isArray(src.gcpProfiles)) {
    sections.gcpProfiles = src.gcpProfiles;
  }
  if (src.instructions && typeof src.instructions === "object") {
    const t = src.instructions.targets;
    sections.instructions = {
      text: typeof src.instructions.text === "string" ? src.instructions.text : "",
      enabled: src.instructions.enabled !== false,
      targets: t && typeof t === "object" && !Array.isArray(t) ? (t as Record<string, boolean>) : {},
    };
  }
  if (!sections.prefs && !sections.ssm && !sections.gcpProfiles && !sections.instructions) return { error: "empty" };
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
  // Names of the sso profiles a chained entry may name as its source: existing ones, plus the
  // ones this plan creates. Chained entries are handled after the sso ones so that order in the
  // file does not matter.
  // exact label -> sign-in of the sso profiles that exist or will exist after this import. A
  // chain joins a source only by exact label AND the same sign-in: AWS profile names are
  // case-sensitive, and a label that merely folds to the same lower-case string may be another
  // account (the profile list itself folds case, so such an sso entry is "exists", not created).
  const ssoByLabel = new Map<string, string>();
  for (const p of existingProfiles || []) if (p?.kind !== "assume_role") ssoByLabel.set(str(p?.label), ssoKey(p));
  const entries = (section.profiles || []).map((raw) => ({
    raw,
    chained: raw?.kind === "assume_role",
  }));
  for (const { raw, chained } of [...entries.filter((e) => !e.chained), ...entries.filter((e) => e.chained)]) {
    const p: SsmProfileEntry = {
      label: str(raw?.label),
      startUrl: str(raw?.startUrl),
      ssoRegion: str(raw?.ssoRegion),
      accountId: str(raw?.accountId),
      roleName: str(raw?.roleName),
      region: str(raw?.region),
    };
    if (chained) {
      p.kind = "assume_role";
      p.source = str(raw?.source);
      p.sourceKey = str(raw?.sourceKey);
      p.roleArn = str(raw?.roleArn);
      p.externalId = str(raw?.externalId);
      p.sessionName = str(raw?.sessionName);
      const d = Number(raw?.durationSeconds);
      if (d > 0) p.durationSeconds = d;
    }
    // Same minimum condition as CP's validateProfile. Without dropping these here the user
    // just sees a row of 400s and cannot tell how many entries were imported.
    const valid = chained
      ? !!p.label && validRoleArn(p.roleArn || "") && validExternalId(p.externalId || "") && validSessionName(p.sessionName || "") && (p.durationSeconds === undefined || validDuration(p.durationSeconds)) && !!p.sourceKey && ssoByLabel.get(p.source || "") === p.sourceKey
      : !!p.label && /^https:\/\/\S+$/.test(p.startUrl) && !!p.ssoRegion;
    if (!valid) {
      plan.skippedProfiles.push({ label: p.label, reason: "invalid" });
      continue;
    }
    if (willHave.has(key(p.label))) {
      plan.skippedProfiles.push({ label: p.label, reason: "exists" });
      continue;
    }
    willHave.add(key(p.label));
    if (!chained) ssoByLabel.set(p.label, ssoKey(p));
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

// --- Import (Google Cloud) -----------------------------------------------------

export interface GcpPlan {
  profiles: GcpProfileEntry[];
  skipped: { label: string; reason: SkipReason }[];
}

/** Narrow the section to the profiles that will be created. Same rules as the AWS profiles:
 *  a label that already exists (case-insensitive) is left alone, and an entry the CP would
 *  refuse for want of a label, a project or a supported login method is skipped with a
 *  reason instead of turning into a row of 400s. */
export function planGcpImport(section: GcpProfileEntry[], existing: any[]): GcpPlan {
  const plan: GcpPlan = { profiles: [], skipped: [] };
  const have = new Set((existing || []).map((p) => key(str(p?.label))));
  for (const raw of section || []) {
    const p = gcpEntry(raw);
    if (!p.label || !p.project || p.loginMethod !== "google") {
      plan.skipped.push({ label: p.label, reason: "invalid" });
      continue;
    }
    if (have.has(key(p.label))) {
      plan.skipped.push({ label: p.label, reason: "exists" });
      continue;
    }
    have.add(key(p.label));
    plan.profiles.push(p);
  }
  return plan;
}

// --- Summary --------------------------------------------------------------------

export interface BundleSummary {
  prefs: number;
  profiles: number;
  hosts: number;
  gcpProfiles: number;
  instructionBytes: number;
  instructions: boolean;
}

export function summarizeBundle(b: SettingsBundle): BundleSummary {
  const s = b.sections;
  return {
    prefs: s.prefs ? Object.keys(s.prefs).length : 0,
    profiles: s.ssm?.profiles.length ?? 0,
    hosts: s.ssm?.hosts.length ?? 0,
    gcpProfiles: s.gcpProfiles?.length ?? 0,
    instructionBytes: s.instructions ? utf8Bytes(s.instructions.text) : 0,
    instructions: !!s.instructions,
  };
}

export function utf8Bytes(s: string): number {
  if (typeof TextEncoder !== "undefined") return new TextEncoder().encode(s).byteLength;
  return s.length;
}
