// awsConfigImport — turns a member's ~/.aws/config into Settings AWS profiles (issue #3010).
//
// Members moving to Agent Fleet already have years of profiles in ~/.aws/config; retyping
// them in Settings was the reason they gave for not moving. The settings bundle (ADR 0060)
// only reads Agent Fleet's own export, so this reads the native file instead and hands the
// result to the same add-only planner (planSsmImport):
//
//   awsConfigIni.parseAwsIni -> readAwsConfig (what each profile is) -> resolveImport (against
//   the member's existing Settings rows and the preview's edits) -> toImportSection -> planSsmImport
//
// Rules this file holds:
//   - Only what the CLI would run as the same identity is created: a complete SSO profile, or
//     a role assumed (one hop) from one. Anything else is "not importable" with a reason; a
//     row is never made importable by dropping the key that made it something else (keys
//     beside sso_*, an inherited credential_process), because that changes who it runs as.
//   - Values are checked against the Agent's own allowlists (agents/ssm/config.go), not just
//     the CP's minimum: the Agent quotes a value it refuses in its sync log, so a value that
//     merely fails there could carry a secret (a key on a continuation line) into a log.
//   - Reasons name keys and lines, never values.
//   - A chain joins an existing Settings row only when the sign-in is provably the same (the
//     file defines the source and its coordinates equal the row's), or when the member picked
//     that row in the preview; never by name alone.
//
// Pure: no fetch, no React.

import { botocoreValue, effectiveOptions, sectionRole, type IniFile, type IniValue } from "./awsConfigIni.ts";
import { awsProfileName, validDuration, validExternalId, validRoleArn, validSessionName } from "./awsRoleChain.ts";
import { ssoKey, type SsmProfileEntry, type SsmSection } from "./settingsBundle.ts";

/** Above this many profiles the preview stops being reviewable; the file is refused whole. */
export const MAX_PROFILES = 500;
/** Paste / file size cap, checked before parsing. */
export const MAX_BYTES = 1024 * 1024;

export const SECRET_KEYS = ["aws_access_key_id", "aws_secret_access_key", "aws_session_token"];
// Ways to get credentials Settings cannot represent. Same list as the Agent's refusals
// (workspace/agent/internal/awsx/exec_nonsso.go).
const UNSUPPORTED_AUTH = ["credential_process", "credential_source", "web_identity_token_file", "mfa_serial", "login_session"];
const KNOWN_KEYS = new Set([
  "sso_session",
  "sso_start_url",
  "sso_region",
  "sso_account_id",
  "sso_role_name",
  "sso_registration_scopes",
  "region",
  "role_arn",
  "source_profile",
  "external_id",
  "role_session_name",
  "duration_seconds",
]);
// The scope the Agent writes for every Settings profile (agents/ssm/config.go).
const AF_SCOPES = "sso:account:access";

// The Agent's allowlists (workspace/agent/internal/agents/ssm/config.go). Anchored and
// without \s, so a multi-line value never passes.
export const RE = {
  startUrl: /^https:\/\/[!-~]+$/,
  region: /^[a-z0-9-]{1,32}$/,
  account: /^[0-9]{1,20}$/,
  role: /^[A-Za-z0-9+=,.@_-]{1,64}$/,
  profile: /^[A-Za-z0-9._@-]{1,64}$/,
  // The Control Plane's rule for a sign-in name (store.SignInNameRe): it becomes af-<name>.
  signIn: /^[A-Za-z0-9._@-]{1,61}$/,
};

export type Reason =
  | { code: "keys" }
  | { code: "auth"; key: string }
  | { code: "mixed_auth" }
  | { code: "no_auth" }
  | { code: "no_session"; session: string }
  | { code: "sso_conflict"; key: string }
  | { code: "incomplete_sso"; missing: string[] }
  | { code: "invalid"; key: string }
  | { code: "no_source_profile" }
  | { code: "self_source" }
  | { code: "multi_hop"; source: string }
  | { code: "source_unsupported"; source: string }
  | { code: "source_unselected"; source: string }
  | { code: "source_missing"; source: string }
  | { code: "bad_duration" }
  | { code: "label_empty" }
  | { code: "name_default" }
  | { code: "name_too_long" }
  | { code: "label_taken"; other: string }
  | { code: "label_dup"; other: string }
  | { code: "name_collision"; other: string }
  // The sign-in name (ADR 0125) is outside the allowlist, names another portal, or is the name
  // of a profile that keeps a sign-in of its own. The name itself is never carried: it came
  // from the file.
  | { code: "sign_in_invalid" }
  | { code: "sign_in_mismatch" }
  | { code: "sign_in_name_taken"; other: string };

interface SsoFields {
  startUrl: string;
  ssoRegion: string;
  accountId: string;
  roleName: string;
}

/** One profile as the CLI defines it, before it meets the member's Settings. */
export interface NativeProfile {
  /** The profile name botocore uses; unique within a file, and the row's identity. Never shown
   *  as is: see `display`. */
  name: string;
  /** `name` when it is safe to show and to propose as a label, else "" (the row is then shown
   *  by its line and needs a label typed in). */
  display: string;
  line: number;
  kind: "sso" | "assume_role" | null;
  /** Why the profile cannot become a Settings profile at all (null = it can). */
  reason: Reason | null;
  sso?: SsoFields;
  /** The laptop's sign-in this profile shares: its sso_session, or the legacy start URL. */
  signIn?: string;
  /** The name the import proposes for the Settings sign-in (ADR 0125): the sso_session's name,
   *  or for a legacy profile the portal's host label. "" = none (own sign-in). */
  signInName?: string;
  /** The sso_session's name is outside the sign-in allowlist, so no name is proposed and the
   *  row needs one typed (the refused text is not kept: it came from the file). */
  signInBad?: boolean;
  roleArn?: string;
  sourceProfile?: string;
  externalId?: string;
  sessionName?: string;
  /** duration_seconds as written ("" = absent), kept only when it is digits; checked in
   *  resolveImport so an edit can fix it. */
  durationRaw?: string;
  /** duration_seconds is set but not digits (its text is not kept: it may be anything). */
  durationBad?: boolean;
  region: string;
  scopesChange: boolean;
  /** Keys this profile sets that Settings does not carry (names only). */
  ignored: string[];
}

export interface NativeConfig {
  profiles: NativeProfile[];
  /** The text holds static keys somewhere (any section, or inside another value). */
  secrets: boolean;
  bomStripped: boolean;
  tooMany: boolean;
}

const has = (o: Map<string, IniValue>, k: string): boolean => o.has(k);
// A name taken from the file (a section header, sso_session, source_profile) that may be shown
// or proposed as a label. One short line holding nothing that looks like a key assignment ("="
// and ":" are both INI delimiters) or a credential key: a header such as
// [profile "aws_secret_access_key=..."] is still a valid profile name to botocore.
export const shownName = (v: string): boolean =>
  /^[^\r\n=:]{1,128}$/.test(v) && ![...SECRET_KEYS, ...UNSUPPORTED_AUTH].some((k) => v.toLowerCase().includes(k));
// The value as the parser returns it. Not trimmed: a value that starts with a newline is a
// nested block to botocore, and trimming would turn its first line into a plausible scalar.
const val = (o: Map<string, IniValue>, k: string): string => o.get(k)?.value ?? "";

const SAFE_KEYS = new Set([
  ...KNOWN_KEYS,
  ...SECRET_KEYS,
  ...UNSUPPORTED_AUTH,
  "output",
  "cli_pager",
  "endpoint_url",
  "services",
  "s3",
  "ca_bundle",
]);
/** A key name from the file, or "" when it is not one this importer knows: key names are text
 *  from the file too, and only known ones are shown. */
export function safeKey(k: string): string {
  return SAFE_KEYS.has(k) ? k : "";
}

/** Map the parsed file to profiles, as botocore's build_profile_map does: a later section
 *  that names the same profile replaces the earlier one. */
export function readAwsConfig(file: IniFile): NativeConfig {
  let secrets = false;
  const scan = (o: Map<string, IniValue>) => {
    for (const [k, v] of o) {
      if (SECRET_KEYS.includes(k) || SECRET_KEYS.some((s) => v.value.toLowerCase().includes(s))) secrets = true;
    }
  };
  scan(file.defaults);
  const profiles = new Map<string, { line: number; opts: Map<string, IniValue> }>();
  const sessions = new Map<string, Map<string, IniValue>>();
  for (const sec of file.sections) {
    scan(sec.options);
    const role = sectionRole(sec.header);
    if (role.kind === "profile") profiles.set(role.name, { line: sec.line, opts: effectiveOptions(file, sec) });
    else if (role.kind === "sso-session") sessions.set(role.name, effectiveOptions(file, sec));
  }
  const out: NativeProfile[] = [];
  for (const [name, p] of profiles) out.push(classify(name, p.line, p.opts, sessions));
  return { profiles: out, secrets, bomStripped: file.bomStripped, tooMany: out.length > MAX_PROFILES };
}

function classify(name: string, line: number, o: Map<string, IniValue>, sessions: Map<string, Map<string, IniValue>>): NativeProfile {
  const region = val(o, "region");
  const base: NativeProfile = {
    name,
    display: shownName(name) ? name : "",
    line,
    kind: null,
    reason: null,
    // Set below once it passed the allowlist, so a refused value is never carried.
    region: "",
    scopesChange: false,
    ignored: [...o.keys()].filter(
      (k) => !KNOWN_KEYS.has(k) && !SECRET_KEYS.includes(k) && !UNSUPPORTED_AUTH.includes(k) && /^[a-z0-9_.-]{1,64}$/.test(k),
    ),
  };
  const fail = (reason: Reason): NativeProfile => ({ ...base, reason });
  const hasKeys = SECRET_KEYS.some((k) => has(o, k));
  const hasSso = [...o.keys()].some((k) => k.startsWith("sso_"));
  const hasRole = has(o, "role_arn");
  const hasProcess = has(o, "credential_process");
  // Any key counts, even empty or where the CLI would not use it: other SDKs read them in a
  // different order, so a name with two methods means two identities (the Agent's policy).
  if ([hasKeys, hasSso, hasRole, hasProcess].filter(Boolean).length > 1) return fail({ code: "mixed_auth" });
  if (hasKeys) return fail({ code: "keys" });
  const unsupported = UNSUPPORTED_AUTH.find((k) => has(o, k));
  if (unsupported) return fail({ code: "auth", key: unsupported });
  if (region && !RE.region.test(region)) return fail({ code: "invalid", key: "region" });
  base.region = region;

  if (hasSso) {
    base.kind = "sso";
    // botocore's SSO provider claims a profile only when the profile itself names an account
    // or a role; a session cannot supply both.
    if (!has(o, "sso_account_id") && !has(o, "sso_role_name")) {
      return fail({ code: "incomplete_sso", missing: ["sso_account_id", "sso_role_name"] });
    }
    // Resolve the session the way botocore does: every key of the session joins the profile's,
    // and a key both give with different values is an error (credentials.py
    // _resolve_sso_session_reference). [DEFAULT] has reached both sides by now.
    let merged = o;
    const sessionName = val(o, "sso_session");
    if (has(o, "sso_session")) {
      if (!shownName(sessionName)) return fail({ code: "invalid", key: "sso_session" });
      const sess = sessions.get(sessionName);
      if (!sess) return fail({ code: "no_session", session: sessionName });
      merged = new Map(o);
      for (const [k, v] of sess) {
        const mine = o.get(k);
        // Compared as botocore sees them: a nested block as a dict, never equal to a string.
        if (mine && botocoreValue(mine.value) !== botocoreValue(v.value)) {
          return fail({ code: "sso_conflict", key: safeKey(k) });
        }
        merged.set(k, v);
      }
      base.signIn = "session:" + sessionName;
      if (RE.signIn.test(sessionName)) base.signInName = sessionName;
      else base.signInBad = true;
    } else {
      base.signIn = "url:" + val(o, "sso_start_url").replace(/\/+$/, "");
    }
    const sso = {
      startUrl: val(merged, "sso_start_url"),
      ssoRegion: val(merged, "sso_region"),
      accountId: val(merged, "sso_account_id"),
      roleName: val(merged, "sso_role_name"),
    };
    const missing = (
      [
        ["sso_start_url", sso.startUrl],
        ["sso_region", sso.ssoRegion],
        ["sso_account_id", sso.accountId],
        ["sso_role_name", sso.roleName],
      ] as const
    )
      .filter(([, v]) => !v)
      .map(([k]) => k);
    if (missing.length) return fail({ code: "incomplete_sso", missing });
    for (const [k, v, re] of [
      ["sso_start_url", sso.startUrl, RE.startUrl],
      ["sso_region", sso.ssoRegion, RE.region],
      ["sso_account_id", sso.accountId, RE.account],
      ["sso_role_name", sso.roleName, RE.role],
    ] as const) {
      if (!re.test(v)) return fail({ code: "invalid", key: k });
    }
    // A legacy profile (no sso_session) gets the portal's host label as its proposed sign-in
    // (ADR 0125 decision B); a host whose label is outside the allowlist proposes none.
    if (!has(o, "sso_session")) base.signInName = hostLabel(sso.startUrl);
    // The service region stays the profile's own (botocore's scoped config, checked above): the
    // session's keys join only the SSO sign-in, so a region only the session sets is not used.
    base.sso = sso;
    const scopes = val(merged, "sso_registration_scopes");
    base.scopesChange =
      !!scopes &&
      scopes
        .split(",")
        .map((x) => x.trim())
        .filter(Boolean)
        .join(",") !== AF_SCOPES;
    return base;
  }
  if (hasRole) {
    base.kind = "assume_role";
    const roleArn = val(o, "role_arn");
    if (!validRoleArn(roleArn)) return fail({ code: "invalid", key: "role_arn" });
    const source = val(o, "source_profile");
    if (!source) return fail({ code: "no_source_profile" });
    if (!shownName(source)) return fail({ code: "invalid", key: "source_profile" });
    if (source === name) return fail({ code: "self_source" });
    const externalId = val(o, "external_id");
    if (!validExternalId(externalId)) return fail({ code: "invalid", key: "external_id" });
    const sessionName = val(o, "role_session_name");
    if (!validSessionName(sessionName)) return fail({ code: "invalid", key: "role_session_name" });
    const d = val(o, "duration_seconds");
    const digits = /^[0-9]{1,9}$/.test(d);
    return { ...base, roleArn, sourceProfile: source, externalId, sessionName, durationRaw: digits ? d : "", durationBad: !!d && !digits };
  }
  return fail({ code: "no_auth" });
}

/** The first label of the portal's host (`d-1234567890` of https://d-1234567890.awsapps.com/start),
 *  or "" when it is not a valid sign-in name. */
export function hostLabel(startUrl: string): string {
  const m = /^https:\/\/([^/:?#]+)/.exec(startUrl);
  const label = m ? m[1].split(".")[0] : "";
  return RE.signIn.test(label) ? label : "";
}

/** A member's change to one row in the preview. */
export interface RowEdit {
  selected?: boolean;
  label?: string;
  /** duration_seconds as typed; "" = leave it to the CLI's default (one hour). */
  duration?: string;
  /** The existing Settings SSO profile chosen as the source of a role whose source_profile the
   *  file does not define. */
  sourceId?: string;
  /** The Settings sign-in name typed for an SSO row ("" = give it a sign-in of its own). */
  signIn?: string;
}

export type RowStatus = "create" | "exists" | "blocked" | "unselected";

export interface ImportRow extends NativeProfile {
  label: string;
  awsName: string;
  selected: boolean;
  status: RowStatus;
  /** For "create" and "exists": the Settings entry (planner shape). */
  entry?: SsmProfileEntry;
  /** For an "exists" row: the existing Settings row it matched. */
  existingId?: string;
  /** The sign-in the SSO row will join or create ("" = its own), after the member's edit. */
  signInName: string;
  /** An existing Settings sign-in of that name on the same portal: the row joins it. */
  signInJoins?: boolean;
  /** For a role row whose source resolved: where it came from. "picked" means the member chose
   *  it, not that the sign-in was verified to be the laptop's. */
  source?: { via: "file" | "existing" | "picked"; label: string };
  /** The region the Agent will use: the explicit one, else the SSO region (or the source's). */
  effectiveRegion: string;
  durationSeconds?: number;
}

export interface ImportPreview {
  rows: ImportRow[];
  /** Existing Settings SSO profiles a role row may be pointed at. */
  sourceChoices: { id: string; label: string; awsName: string; startUrl: string; ssoRegion: string; accountId: string; roleName: string }[];
  /** New logins the import implies: one per new sign-in plus one per created SSO profile with a
   *  sign-in of its own (a role signs in through its source). */
  logins: number;
  /** The sign-in list could not be read: no sign-in is proposed and the rows get their own. */
  signInsUnknown: boolean;
  /** How many sign-ins those profiles shared on the laptop. */
  laptopSignIns: number;
}

const ci = (s: string) => s.trim().toLowerCase();

type Source = { via: "file" | "existing" | "picked"; label: string; key: string; existingId?: string; region: string };

/** Resolve the native profiles against the member's existing Settings profiles (the CP list,
 *  as returned by GET api/ssm/profiles) and the preview's edits. Re-run on every edit. */
export function resolveImport(
  cfg: NativeConfig,
  existing: any[],
  edits: Record<string, RowEdit>,
  signIns: ExistingSignIn[] | null = [],
): ImportPreview {
  // null = the member's sign-ins could not be read (an older Control Plane, or a failed GET):
  // nothing is proposed or sent, so no row is silently grouped under a name nobody checked.
  const signInsByName = new Map((signIns || []).map((si) => [si.name, si]));
  const ex = (existing || []).map((p) => ({
    id: String(p?.id ?? ""),
    label: String(p?.label ?? "").trim(),
    kind: p?.kind === "assume_role" ? "assume_role" : "sso",
    key: ssoKey(p),
    roleArn: String(p?.roleArn ?? ""),
    sourceProfileId: String(p?.sourceProfileId ?? ""),
    region: String(p?.region ?? ""),
    ssoRegion: String(p?.ssoRegion ?? ""),
    raw: p,
  }));
  // The CP does not keep labels unique, so one label may name several rows.
  const exByLabel = new Map<string, typeof ex>();
  for (const e of ex) exByLabel.set(ci(e.label), [...(exByLabel.get(ci(e.label)) || []), e]);
  const exById = new Map(ex.map((e) => [e.id, e]));
  const byName = new Map(cfg.profiles.map((p) => [p.name, p]));
  const keyOf = (p: NativeProfile) => ssoKey(p.sso);

  // What keeps an SSO row from joining or creating the sign-in it names, judged against the
  // member's existing Settings rows only (the clash among the creations is clashesOf's).
  const signInReason = (row: ImportRow, p: NativeProfile, e: RowEdit | undefined): Reason | null => {
    const name = row.signInName;
    if (e?.signIn === undefined && p.signInBad) return { code: "sign_in_invalid" };
    if (!name) return null;
    if (!RE.signIn.test(name)) return { code: "sign_in_invalid" };
    const have = signInsByName.get(name);
    if (have) {
      if (have.startUrl !== p.sso!.startUrl || have.ssoRegion !== p.sso!.ssoRegion) return { code: "sign_in_mismatch" };
      row.signInJoins = true;
    }
    // A profile that keeps a sign-in of its own renders the same sso-session name.
    const own = ex.find((x) => x.kind === "sso" && !x.raw?.signIn && awsProfileName(x.label) === name);
    return own ? { code: "sign_in_name_taken", other: own.label } : null;
  };

  // One pass of the resolution, with `clashed` = rows blocked by a clash among the creations.
  const pass = (clashed: Map<string, Reason>): ImportRow[] => {
    const rowOf = new Map<string, ImportRow>();
    const base = (p: NativeProfile): ImportRow => {
      const e = edits[p.name] || {};
      const label = (e.label ?? p.display).trim();
      const awsName = awsProfileName(label);
      const typed = e.signIn !== undefined ? e.signIn.trim() : undefined;
      const signInName = signIns && p.kind === "sso" ? (typed ?? p.signInName ?? "") : "";
      const row: ImportRow = { ...p, label, awsName, signInName, selected: e.selected ?? true, status: "blocked", effectiveRegion: p.region };
      if (p.reason) return { ...row, reason: p.reason };
      if (!label) return { ...row, reason: { code: "label_empty" } };
      if (awsName === "default") return { ...row, reason: { code: "name_default" } };
      if (!RE.profile.test(awsName)) return { ...row, reason: { code: "name_too_long" } };
      // An existing row under another label that becomes the same AWS name: both would be left
      // out of ~/.aws (aws_profiles_bridge.go). Independent of the other rows, so settled here.
      const twin = ex.find((x) => ci(x.label) !== ci(label) && awsProfileName(x.label) === awsName);
      if (twin) return { ...row, reason: { code: "name_collision", other: twin.label } };
      return row;
    };
    // A row that would be created, unless it clashes with another such row. The clash is
    // applied last so that a row whose source fell away is not kept in the clash pool.
    const finish = (row: ImportRow, entry: SsmProfileEntry) => {
      const c = row.selected ? clashed.get(row.name) : undefined;
      if (c) Object.assign(row, { reason: c, entry });
      else Object.assign(row, { status: row.selected ? "create" : "unselected", entry });
    };
    // SSO rows first: role rows depend on their outcome.
    for (const p of cfg.profiles) {
      if (p.kind !== "sso") continue;
      const row = base(p);
      rowOf.set(p.name, row);
      if (row.reason) continue;
      row.effectiveRegion = p.region || p.sso!.ssoRegion;
      const entry: SsmProfileEntry = { label: row.label, ...p.sso!, region: p.region, ...(row.signInName ? { signIn: row.signInName } : {}) };
      const hits = exByLabel.get(ci(row.label));
      if (hits) {
        const same = hits.find((h) => h.kind === "sso" && h.key === keyOf(p));
        if (same) {
          // The existing row is what will be used, so its region is the one that counts.
          row.effectiveRegion = same.region || same.ssoRegion;
          Object.assign(row, { status: "exists", entry: { ...entry, label: same.label }, existingId: same.id });
        } else row.reason = { code: "label_taken", other: hits[0].label };
        continue;
      }
      const bad = row.selected && signIns ? signInReason(row, p, edits[p.name]) : null;
      if (bad) {
        Object.assign(row, { reason: bad, entry });
        continue;
      }
      finish(row, entry);
    }
    for (const p of cfg.profiles) {
      if (p.kind === "sso") continue;
      const row = base(p);
      rowOf.set(p.name, row);
      if (row.reason || p.kind !== "assume_role") continue;
      const e = edits[p.name] || {};
      let source: Source | null = null;
      const def = byName.get(p.sourceProfile!);
      if (def) {
        const s = rowOf.get(def.name);
        const shown = def.display;
        if (def.kind === "assume_role") row.reason = { code: "multi_hop", source: shown };
        else if (!s || s.reason || def.kind !== "sso") row.reason = { code: "source_unsupported", source: shown };
        else if (s.status === "unselected") row.reason = { code: "source_unselected", source: shown };
        else
          source = {
            via: s.status === "exists" ? "existing" : "file",
            label: s.entry!.label,
            key: keyOf(def),
            existingId: s.existingId,
            region: s.effectiveRegion,
          };
      } else {
        const picked = e.sourceId ? exById.get(e.sourceId) : undefined;
        if (picked && picked.kind === "sso") {
          source = {
            via: "picked",
            label: picked.label,
            key: picked.key,
            existingId: picked.id,
            region: picked.region || picked.ssoRegion,
          };
        } else {
          row.reason = { code: "source_missing", source: p.sourceProfile! };
        }
      }
      if (!source) continue;
      row.source = { via: source.via, label: source.label };
      row.effectiveRegion = p.region || source.region;
      // Duration: the edit wins over the file. Dropping a bad value silently would mean the
      // CLI's one-hour default, which is a different session length, so it blocks instead.
      const dRaw = e.duration !== undefined ? e.duration.trim() : p.durationBad ? null : (p.durationRaw ?? "");
      let duration: number | undefined;
      if (dRaw === null) {
        row.reason = { code: "bad_duration" };
        continue;
      }
      if (dRaw) {
        const n = /^[0-9]+$/.test(dRaw) ? Number(dRaw) : NaN;
        if (!validDuration(n)) {
          row.reason = { code: "bad_duration" };
          continue;
        }
        duration = n;
      }
      row.durationSeconds = duration;
      const entry: SsmProfileEntry = {
        kind: "assume_role",
        label: row.label,
        startUrl: "",
        ssoRegion: "",
        // The CP derives the account from the role ARN and refuses a different one.
        accountId: "",
        roleName: "",
        region: p.region,
        source: source.label,
        sourceKey: source.key,
        // The row matched or picked, carried to the executor so a label shared by two rows
        // cannot send the chain to the other one.
        ...(source.existingId ? { sourceId: source.existingId } : {}),
        roleArn: p.roleArn!,
        externalId: p.externalId!,
        sessionName: p.sessionName!,
        ...(duration !== undefined ? { durationSeconds: duration } : {}),
      };
      const hits = exByLabel.get(ci(row.label));
      if (hits) {
        const same = hits.find(
          (h) => h.kind === "assume_role" && h.roleArn === p.roleArn && !!source!.existingId && h.sourceProfileId === source!.existingId,
        );
        if (same) Object.assign(row, { status: "exists", entry: { ...entry, label: same.label }, existingId: same.id });
        else row.reason = { code: "label_taken", other: hits[0].label };
        continue;
      }
      finish(row, entry);
    }
    return cfg.profiles.map((p) => rowOf.get(p.name)!);
  };

  // Clashes among the creations. Labels compare case-insensitively (the planner's rule), AWS
  // names exactly (the CLI's). They are recomputed from scratch on every pass, among the rows
  // that would be created but for their own clash, because blocking one row can drop a
  // dependent that was the other side of another clash.
  const clashesOf = (rows: ImportRow[], prev: Map<string, Reason>): Map<string, Reason> => {
    const pool = rows.filter((r) => r.status === "create" || (prev.has(r.name) && r.reason === prev.get(r.name)));
    const out = new Map<string, Reason>();
    for (const r of pool) {
      for (const o of pool) {
        if (o === r || out.has(r.name)) continue;
        if (ci(o.label) === ci(r.label)) out.set(r.name, { code: "label_dup", other: o.display || o.label });
        else if (o.awsName === r.awsName) out.set(r.name, { code: "name_collision", other: o.label });
      }
    }
    // Sign-ins among the creations: one name has one portal, and no name is also the name of a
    // profile that keeps a sign-in of its own (the CP refuses both, ADR 0125 decisions 1 and 3).
    for (const r of pool) {
      if (out.has(r.name) || r.kind !== "sso" || !r.signInName) continue;
      const portal = (x: ImportRow) => x.sso!.startUrl + "\n" + x.sso!.ssoRegion;
      if (pool.some((o) => o !== r && o.kind === "sso" && o.signInName === r.signInName && portal(o) !== portal(r))) {
        out.set(r.name, { code: "sign_in_mismatch" });
        continue;
      }
      const own = pool.find((o) => o.kind === "sso" && !o.signInName && o.awsName === r.signInName);
      if (own) out.set(r.name, { code: "sign_in_name_taken", other: own.label });
    }
    return out;
  };
  const same = (a: Map<string, Reason>, b: Map<string, Reason>) => a.size === b.size && [...a.keys()].every((k) => b.has(k));
  let clashed = new Map<string, Reason>();
  let before = new Map<string, Reason>();
  let rows = pass(clashed);
  for (let i = 0; i <= cfg.profiles.length + 1; i++) {
    const next = clashesOf(rows, clashed);
    if (same(next, clashed)) break;
    // Two states taking turns: settle on blocking both sides rather than one at random.
    if (same(next, before)) {
      next.forEach((v, k) => clashed.set(k, v));
      rows = pass(clashed);
      break;
    }
    before = clashed;
    clashed = next;
    rows = pass(clashed);
  }
  for (const r of rows) if (r.reason) r.status = "blocked";

  const created = rows.filter((r) => r.status === "create" && r.kind === "sso");
  return {
    rows,
    sourceChoices: ex
      .filter((e) => e.kind === "sso")
      .map((e) => ({
        id: e.id,
        label: e.label,
        awsName: awsProfileName(e.label),
        startUrl: String(e.raw?.startUrl ?? ""),
        ssoRegion: e.ssoRegion,
        accountId: String(e.raw?.accountId ?? ""),
        roleName: String(e.raw?.roleName ?? ""),
      })),
    logins: loginsOf(created, signIns),
    signInsUnknown: signIns === null,
    laptopSignIns: new Set(created.map((r) => r.signIn)).size,
  };
}

/** The member's sign-in as GET api/ssm/sign-ins lists it (only what the preview compares). */
export interface ExistingSignIn {
  name: string;
  startUrl: string;
  ssoRegion: string;
}

/** The logins the SSO rows need: one per sign-in that does not exist yet, plus one per row with a
 *  sign-in of its own. A row that joins an existing sign-in needs none of its own. */
export function loginsOf(rows: { signInName: string }[], signIns: ExistingSignIn[] | null): number {
  const have = new Set((signIns || []).map((si) => si.name));
  const fresh = new Set(rows.filter((r) => r.signInName && !have.has(r.signInName)).map((r) => r.signInName));
  return fresh.size + rows.filter((r) => !r.signInName).length;
}

/** The rows to create, in the planner's shape. */
export function toImportSection(preview: ImportPreview): SsmSection {
  return { profiles: preview.rows.filter((r) => r.status === "create").map((r) => r.entry!), hosts: [] };
}
