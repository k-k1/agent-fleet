// gcloudConfigImport — turns gcloud configuration files into Settings → Google Cloud profile
// rows (issue #3017, part 2). Pure: no fetch, no React.
//
// A configuration (~/.config/gcloud/configurations/config_<name>) is read by gcloud with
// Python's ConfigParser defaults, so it goes through the same reader as ~/.aws/config
// (awsConfigIni.ts, without the botocore-only step). Only four properties map to a profile:
// core/account, core/project, billing/quota_project, compute/region and compute/zone, plus
// auth/impersonate_service_account when it is a single service account.
//
// Not 1:1, and what is refused rather than approximated (a refusal names a key and never a
// value; a path or token reference in a file is never shown or sent):
//   - the profile logs in with a Google account; a service account as core/account would need a
//     key, so it is refused (impersonation is the supported way);
//   - auth overrides (credential file, access token file, custom OAuth client or hosts), a
//     comma-separated impersonation list (a delegation chain), proxy and API endpoint overrides,
//     a non-default universe domain: Settings cannot express them, and importing without them
//     would point the profile at something the laptop's configuration does not.
// The value checks mirror the CP's validateGCPProfile (control-plane/gcp_profiles.go) exactly,
// so a row that previews as importable is not a 400 later.
import { effectiveOptions, parseIni, type IniFile, type IniError } from "./awsConfigIni.ts";

/** Per-file size cap: a configuration is a few lines. */
export const MAX_GCP_FILE_BYTES = 64 * 1024;
export const MAX_GCP_FILES = 200;

// ---- The CP's checks (control-plane/gcp_profiles.go). Go's `\s` is ASCII-only: spelled out. ----
const WS = "\\t\\n\\f\\r ";
export const GCP_PROJECT_RE = /^(?:[a-z0-9][a-z0-9.-]*\.[a-z]{2,}:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$/;
export const GCP_EMAIL_RE = new RegExp(`^[^${WS}@,;"'<>]+@[a-zA-Z0-9-]+(?:\\.[a-zA-Z0-9-]+)+$`);
export const GCP_SERVICE_ACCOUNT_RE = /^[a-z0-9][a-z0-9-]{0,99}@(?:[a-z0-9:.-]+\.)?gserviceaccount\.com$/;
export const GCP_REGION_RE = /^[a-z]+(?:-[a-z]+)+[0-9]+$/;
export const GCP_ZONE_RE = /^[a-z]+(?:-[a-z]+)+[0-9]+-[a-z]$/;
const KEY_MATERIAL_RE = new RegExp(`"?private_key(?:_id)?"?[${WS}]*:|"type"[${WS}]*:[${WS}]*"service_account"|-----BEGIN`, "i");
const LABEL_MAX = 100;

/** A label this importer creates. It is a subset of what the CP accepts, chosen so that the CP's
 *  derived profile name (gcpProfileName) is the label itself: no collision can hide behind a
 *  case or punctuation difference that the Console would have to re-derive. */
export const GCP_IMPORT_LABEL_RE = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;
// gcloud's own rule for a configuration name (named_configs.py _VALID_CONFIG_NAME_REGEX): a
// trailing hyphen is valid there, and is fixed at the label step instead.
const CONFIG_NAME_RE = /^[a-z][-a-z0-9]*$/;

// Keys a reason may name: file text is never echoed, so a key not listed here shows as "?".
const SAFE_KEYS = new Set([
  "core/account",
  "core/project",
  "core/universe_domain",
  "core/custom_ca_certs_file",
  "billing/quota_project",
  "compute/region",
  "compute/zone",
  "auth/impersonate_service_account",
  "auth/credential_file_override",
  "auth/access_token_file",
  "auth/authorization_token_file",
  "auth/disable_credentials",
  "auth/client_id",
  "auth/client_secret",
  "auth/auth_host",
  "auth/token_host",
  "auth/authority_selector",
]);
export function safeGcpKey(k: string): string {
  return SAFE_KEYS.has(k) ? k : "";
}

// Any non-empty auth/ key other than impersonate_service_account is refused: a credential file,
// an access token file, a custom OAuth client or host change who signs in or where, and a
// setting this importer has not looked at cannot be judged harmless. disable_credentials is
// the one switch that is harmless when false.
// gcloud's boolean validator: these spell true, these false, and anything else is an error there.
const TRUE_RE = /^(?:true|1|on|yes|y)$/i;
const FALSE_RE = /^(?:false|0|off|no|n|none)$/i;

export type GcpReason =
  | { code: "not_config" }
  | { code: "bad_name" }
  | { code: "parse"; error: IniError }
  | { code: "no_project" }
  | { code: "invalid"; key: string }
  | { code: "key_material" }
  | { code: "sa_account" }
  | { code: "delegation" }
  | { code: "auth_override"; key: string }
  | { code: "endpoint_override" }
  | { code: "proxy" }
  | { code: "universe"; key: string }
  | { code: "label_empty" }
  | { code: "label_bad" }
  | { code: "interpolation" }
  | { code: "label_taken"; other: string }
  | { code: "label_conflict" }
  | { code: "label_dup"; other: string };

export interface GcpFields {
  project: string;
  quotaProject: string;
  account: string;
  region: string;
  zone: string;
  impersonateServiceAccount: string;
}

/** One file (or pasted text) after reading: what it says, or why it cannot be imported. */
export interface NativeGcpConfig {
  /** Position in the source list; the row's identity. */
  index: number;
  /** The configuration name; null when the file is not a configuration at all. */
  name: string | null;
  fields: GcpFields | null;
  /** A reason that comes from the file itself. */
  reason: GcpReason | null;
  /** Properties present that Settings does not hold (and that are harmless to leave out). */
  ignored: number;
  bomStripped: boolean;
}

const val = (o: Map<string, { value: string }>, k: string) => o.get(k)?.value.trim() ?? "";

/** Map one parsed configuration to a profile's fields, or to the reason it is refused. */
export function readGcloudConfig(file: IniFile, index: number, name: string): NativeGcpConfig {
  const base = { index, name, ignored: 0, bomStripped: file.bomStripped };
  const refuse = (reason: GcpReason, ignored = 0): NativeGcpConfig => ({ ...base, fields: null, reason, ignored });
  const sec = new Map<string, Map<string, { value: string; line: number }>>();
  for (const s of file.sections) sec.set(s.header, effectiveOptions(file, s));
  const get = (section: string, key: string) => val(sec.get(section) ?? new Map(), key);

  // gcloud reads with ConfigParser's BasicInterpolation: `%(name)s` and `%%` in a value are
  // expanded (or are an error), so the text is not the value gcloud uses. Not reimplemented:
  // a file that relies on it is refused as a whole rather than imported as something else.
  for (const o of sec.values()) {
    for (const v of o.values()) if (v.value.includes("%")) return refuse({ code: "interpolation" });
  }

  // Refusals first, in the order a member would fix them: what the file does to sign-in, then
  // where requests go, then the values themselves.
  for (const [k, v] of sec.get("auth") ?? []) {
    if (k === "impersonate_service_account" || v.value.trim() === "") continue;
    if (k === "disable_credentials") {
      const b = v.value.trim();
      if (FALSE_RE.test(b)) continue;
      if (!TRUE_RE.test(b)) return refuse({ code: "invalid", key: "auth/disable_credentials" });
    }
    return refuse({ code: "auth_override", key: safeGcpKey("auth/" + k) || "?" });
  }
  const imp = get("auth", "impersonate_service_account");
  if (imp.includes(",")) return refuse({ code: "delegation" });
  for (const [header, o] of sec) {
    if (header === "api_endpoint_overrides" && [...o.values()].some((v) => v.value.trim() !== "")) return refuse({ code: "endpoint_override" });
    if (header === "proxy" && [...o.values()].some((v) => v.value.trim() !== "")) return refuse({ code: "proxy" });
  }
  const universe = get("core", "universe_domain");
  if (universe && universe !== "googleapis.com") return refuse({ code: "universe", key: "core/universe_domain" });
  if (get("core", "custom_ca_certs_file")) return refuse({ code: "universe", key: "core/custom_ca_certs_file" });

  const fields: GcpFields = {
    project: get("core", "project"),
    quotaProject: get("billing", "quota_project"),
    account: get("core", "account"),
    region: get("compute", "region"),
    zone: get("compute", "zone"),
    impersonateServiceAccount: imp,
  };
  const checked: [keyof GcpFields, string][] = [
    ["project", "core/project"],
    ["quotaProject", "billing/quota_project"],
    ["account", "core/account"],
    ["region", "compute/region"],
    ["zone", "compute/zone"],
    ["impersonateServiceAccount", "auth/impersonate_service_account"],
  ];
  if (checked.some(([f]) => KEY_MATERIAL_RE.test(fields[f]))) return refuse({ code: "key_material" });
  if (!fields.project) return refuse({ code: "no_project" });
  if (!GCP_PROJECT_RE.test(fields.project)) return refuse({ code: "invalid", key: "core/project" });
  if (fields.quotaProject && !GCP_PROJECT_RE.test(fields.quotaProject)) return refuse({ code: "invalid", key: "billing/quota_project" });
  if (fields.account) {
    if (new TextEncoder().encode(fields.account).byteLength > 254 || !GCP_EMAIL_RE.test(fields.account)) {
      return refuse({ code: "invalid", key: "core/account" });
    }
    // The CP accepts it, but a service account cannot sign in without a key.
    if (/@(?:[^@]*\.)?gserviceaccount\.com$/i.test(fields.account)) return refuse({ code: "sa_account" });
  }
  if (fields.impersonateServiceAccount && !GCP_SERVICE_ACCOUNT_RE.test(fields.impersonateServiceAccount)) {
    return refuse({ code: "invalid", key: "auth/impersonate_service_account" });
  }
  if (fields.region && !GCP_REGION_RE.test(fields.region)) return refuse({ code: "invalid", key: "compute/region" });
  if (fields.zone && !GCP_ZONE_RE.test(fields.zone)) return refuse({ code: "invalid", key: "compute/zone" });

  const used = new Set(checked.map(([, k]) => k));
  let ignored = 0;
  for (const [header, o] of sec) for (const [k, v] of o) if (v.value.trim() !== "" && !used.has(header + "/" + k)) ignored++;
  return { ...base, fields, reason: null, ignored };
}

/** The configuration name of a file called `config_<name>`; null for any other file name
 *  (`active_config`, `.DS_Store`, …). */
export function configNameOfFile(fileName: string): string | null {
  const m = /^config_(.+)$/.exec(fileName);
  return m ? m[1] : null;
}

/** One source (a file or the pasted text) → one native configuration. `name` is null for a file
 *  that is not a configuration. */
export function readGcloudSource(index: number, name: string | null, text: string): NativeGcpConfig {
  if (name === null) return { index, name: null, fields: null, reason: { code: "not_config" }, ignored: 0, bomStripped: false };
  if (!CONFIG_NAME_RE.test(name)) return { index, name: null, fields: null, reason: { code: "bad_name" }, ignored: 0, bomStripped: false };
  const parsed = parseIni(text, false);
  if (!parsed.ok) return { index, name, fields: null, reason: { code: "parse", error: parsed.error }, ignored: 0, bomStripped: false };
  return readGcloudConfig(parsed.file, index, name);
}

// ---- Preview ----

export type GcpStatus = "create" | "exists" | "unselected" | "blocked";

export interface GcpRowEdit {
  selected?: boolean;
  label?: string;
}

export interface GcpImportRow {
  index: number;
  /** The configuration name, or null when it is not safe to show. */
  display: string | null;
  label: string;
  fields: GcpFields | null;
  status: GcpStatus;
  reason: GcpReason | null;
  selected: boolean;
  ignored: number;
}

/** The slice of a GET api/gcp/profiles row the preview needs. */
export interface ExistingGcpProfile {
  label?: unknown;
  name?: unknown;
  loginMethod?: unknown;
  project?: unknown;
  quotaProject?: unknown;
  account?: unknown;
  region?: unknown;
  zone?: unknown;
  impersonateServiceAccount?: unknown;
  /** Set by the CP when the row's name collides with another's. */
  conflict?: unknown;
}

const s = (v: unknown) => (typeof v === "string" ? v.trim() : "");
const sameFields = (e: ExistingGcpProfile, f: GcpFields) =>
  (s(e.loginMethod) || "google") === "google" &&
  s(e.project) === f.project &&
  s(e.quotaProject) === f.quotaProject &&
  s(e.account) === f.account &&
  s(e.region) === f.region &&
  s(e.zone) === f.zone &&
  s(e.impersonateServiceAccount) === f.impersonateServiceAccount;

/** Rows in file order, each created / already there / blocked with a reason. Runs again on every
 *  edit and again right before a write, against the list read at that moment. */
export function resolveGcpImport(cfgs: NativeGcpConfig[], existing: ExistingGcpProfile[], edits: Record<number, GcpRowEdit>) {
  const rows: GcpImportRow[] = [];
  const taken = new Map<string, string>(); // label of a row that will be created -> its display
  for (const c of cfgs) {
    const e = edits[c.index] ?? {};
    const label = (e.label ?? c.name ?? "").trim();
    const selected = e.selected ?? true;
    const display = c.name && CONFIG_NAME_RE.test(c.name) ? c.name : null;
    const row: GcpImportRow = { index: c.index, display, label, fields: c.fields, status: "create", reason: null, selected, ignored: c.ignored };
    rows.push(row);
    if (c.reason || !c.fields) {
      row.status = "blocked";
      row.reason = c.reason;
      continue;
    }
    if (!label) {
      row.status = "blocked";
      row.reason = { code: "label_empty" };
      continue;
    }
    if (!GCP_IMPORT_LABEL_RE.test(label) || [...label].length > LABEL_MAX) {
      row.status = "blocked";
      row.reason = { code: "label_bad" };
      continue;
    }
    const hits = existing.filter((p) => s(p.label).toLowerCase() === label || s(p.name) === label);
    const hit = hits[0];
    if (hit) {
      // Two rows with one name are both left out of the workspace by the CP: neither is a usable
      // registered row, so the label has to change.
      if (hits.length > 1 || hits.some((h) => h.conflict)) {
        row.status = "blocked";
        row.reason = { code: "label_conflict" };
      } else if (sameFields(hit, c.fields)) row.status = "exists";
      else {
        row.status = "blocked";
        row.reason = { code: "label_taken", other: s(hit.label) };
      }
      continue;
    }
    if (!selected) {
      row.status = "unselected";
      continue;
    }
    const dup = taken.get(label);
    if (dup !== undefined) {
      row.status = "blocked";
      row.reason = { code: "label_dup", other: dup };
      continue;
    }
    taken.set(label, display ?? label);
  }
  return { rows };
}

/** What the executor sends, for the rows a plan creates: the CP's own field set, nothing else. */
export interface GcpCreate {
  label: string;
  fields: GcpFields;
}
export function planGcpCreates(rows: GcpImportRow[]): GcpCreate[] {
  return rows.filter((r) => r.status === "create" && r.fields).map((r) => ({ label: r.label, fields: r.fields! }));
}
