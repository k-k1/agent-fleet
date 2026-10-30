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

import { effectiveOptions, sectionRole, type IniFile, type IniValue } from "./awsConfigIni.ts";
import { awsProfileName, validDuration, validExternalId, validRoleArn, validSessionName } from "./awsRoleChain.ts";
import { ssoKey, type SsmProfileEntry, type SsmSection } from "./settingsBundle.ts";

/** Above this many profiles the preview stops being reviewable; the file is refused whole. */
export const MAX_PROFILES = 500;
/** Paste / file size cap, checked before parsing. */
export const MAX_BYTES = 1024 * 1024;

const SECRET_KEYS = ["aws_access_key_id", "aws_secret_access_key", "aws_session_token"];
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
const RE = {
  startUrl: /^https:\/\/[!-~]+$/,
  region: /^[a-z0-9-]{1,32}$/,
  account: /^[0-9]{1,20}$/,
  role: /^[A-Za-z0-9+=,.@_-]{1,64}$/,
  profile: /^[A-Za-z0-9._@-]{1,64}$/,
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
  | { code: "name_collision"; other: string };

interface SsoFields {
  startUrl: string;
  ssoRegion: string;
  accountId: string;
  roleName: string;
}

/** One profile as the CLI defines it, before it meets the member's Settings. */
export interface NativeProfile {
  /** The profile name botocore uses; unique within a file, and the row's identity. */
  name: string;
  line: number;
  kind: "sso" | "assume_role" | null;
  /** Why the profile cannot become a Settings profile at all (null = it can). */
  reason: Reason | null;
  sso?: SsoFields;
  /** The laptop's sign-in this profile shares: its sso_session, or the legacy start URL. */
  signIn?: string;
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
// A name read from a value (sso_session, source_profile) that is shown back in a reason. One
// line and short, or it is not shown: a continuation line can hold anything, keys included.
const shownName = (v: string): boolean => /^[^\r\n]{1,128}$/.test(v) && !SECRET_KEYS.some((k) => v.toLowerCase().includes(k));
const val = (o: Map<string, IniValue>, k: string): string => o.get(k)?.value.trim() ?? "";

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
    let startUrl = val(o, "sso_start_url");
    let ssoRegion = val(o, "sso_region");
    let scopes = val(o, "sso_registration_scopes");
    const sessionName = val(o, "sso_session");
    if (has(o, "sso_session")) {
      const s = sessions.get(sessionName);
      if (!shownName(sessionName)) return fail({ code: "invalid", key: "sso_session" });
      if (!s) return fail({ code: "no_session", session: sessionName });
      // botocore refuses a profile whose own sso_start_url / sso_region disagree with its session's.
      for (const k of ["sso_start_url", "sso_region"]) {
        if (has(o, k) && has(s, k) && val(o, k) !== val(s, k)) return fail({ code: "sso_conflict", key: k });
      }
      startUrl = val(s, "sso_start_url") || startUrl;
      ssoRegion = val(s, "sso_region") || ssoRegion;
      scopes = val(s, "sso_registration_scopes") || scopes;
      base.signIn = "session:" + sessionName;
    } else {
      base.signIn = "url:" + startUrl.replace(/\/+$/, "");
    }
    const sso = { startUrl, ssoRegion, accountId: val(o, "sso_account_id"), roleName: val(o, "sso_role_name") };
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
    base.sso = sso;
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

/** A member's change to one row in the preview. */
export interface RowEdit {
  selected?: boolean;
  label?: string;
  /** duration_seconds as typed; "" = leave it to the CLI's default (one hour). */
  duration?: string;
  /** The existing Settings SSO profile chosen as the source of a role whose source_profile the
   *  file does not define. */
  sourceId?: string;
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
  /** New independent logins the import implies: one per created SSO profile (a role signs in
   *  through its source). */
  logins: number;
  /** How many sign-ins those profiles shared on the laptop. */
  laptopSignIns: number;
}

const ci = (s: string) => s.trim().toLowerCase();

/** Resolve the native profiles against the member's existing Settings profiles (the CP list,
 *  as returned by GET api/ssm/profiles) and the preview's edits. Re-run on every edit. */
export function resolveImport(cfg: NativeConfig, existing: any[], edits: Record<string, RowEdit>): ImportPreview {
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
  const exByLabel = new Map(ex.map((e) => [ci(e.label), e]));
  const exById = new Map(ex.map((e) => [e.id, e]));
  const byName = new Map(cfg.profiles.map((p) => [p.name, p]));
  // Rows blocked by a name clash. Only ever grows, so the loop below ends.
  const clashed = new Map<string, Reason>();

  const keyOf = (p: NativeProfile) => ssoKey(p.sso);
  let rows: ImportRow[] = [];
  for (let pass = 0; pass <= cfg.profiles.length + 1; pass++) {
    const rowOf = new Map<string, ImportRow>();
    const base = (p: NativeProfile): ImportRow => {
      const e = edits[p.name] || {};
      const label = (e.label ?? p.name).trim();
      const awsName = awsProfileName(label);
      const row: ImportRow = { ...p, label, awsName, selected: e.selected ?? true, status: "blocked", effectiveRegion: p.region };
      if (p.reason) return { ...row, reason: p.reason };
      if (!label) return { ...row, reason: { code: "label_empty" } };
      if (awsName === "default") return { ...row, reason: { code: "name_default" } };
      if (!RE.profile.test(awsName)) return { ...row, reason: { code: "name_too_long" } };
      const c = clashed.get(p.name);
      if (c) return { ...row, reason: c };
      return row;
    };
    // SSO rows first: role rows depend on their outcome.
    for (const p of cfg.profiles) {
      if (p.kind !== "sso") continue;
      const row = base(p);
      rowOf.set(p.name, row);
      if (row.reason) continue;
      row.effectiveRegion = p.region || p.sso!.ssoRegion;
      const entry: SsmProfileEntry = { label: row.label, ...p.sso!, region: p.region };
      const hit = exByLabel.get(ci(row.label));
      if (hit) {
        if (hit.kind === "sso" && hit.key === keyOf(p))
          Object.assign(row, { status: "exists", entry: { ...entry, label: hit.label }, existingId: hit.id });
        else row.reason = { code: "label_taken", other: hit.label };
        continue;
      }
      Object.assign(row, { status: row.selected ? "create" : "unselected", entry });
    }
    for (const p of cfg.profiles) {
      if (p.kind === "sso") continue;
      const row = base(p);
      rowOf.set(p.name, row);
      if (row.reason || p.kind !== "assume_role") continue;
      const e = edits[p.name] || {};
      // Source.
      let source: { via: "file" | "existing" | "picked"; label: string; key: string; existingId?: string; region: string } | null = null;
      const def = byName.get(p.sourceProfile!);
      if (def) {
        const s = rowOf.get(def.name);
        if (def.kind === "assume_role") row.reason = { code: "multi_hop", source: def.name };
        else if (!s || s.reason || def.kind !== "sso") row.reason = { code: "source_unsupported", source: def.name };
        else if (s.status === "unselected") row.reason = { code: "source_unselected", source: def.name };
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
        roleArn: p.roleArn!,
        externalId: p.externalId!,
        sessionName: p.sessionName!,
        ...(duration !== undefined ? { durationSeconds: duration } : {}),
      };
      const hit = exByLabel.get(ci(row.label));
      if (hit) {
        if (hit.kind === "assume_role" && hit.roleArn === p.roleArn && !!source.existingId && hit.sourceProfileId === source.existingId) {
          Object.assign(row, { status: "exists", entry: { ...entry, label: hit.label }, existingId: hit.id });
        } else row.reason = { code: "label_taken", other: hit.label };
        continue;
      }
      Object.assign(row, { status: row.selected ? "create" : "unselected", entry });
    }
    rows = cfg.profiles.map((p) => rowOf.get(p.name)!);
    // Name clashes among what will exist afterwards: the existing rows plus the creations. An
    // existing row a creation matched as "exists" is that row, so it is not counted twice.
    // Labels compare case-insensitively (the planner's rule), AWS names exactly (the CLI's).
    let grew = false;
    const creating = rows.filter((r) => r.status === "create");
    const block = (r: ImportRow, reason: Reason) => {
      if (!clashed.has(r.name)) {
        clashed.set(r.name, reason);
        grew = true;
      }
    };
    for (const r of creating) {
      for (const o of creating) {
        if (o === r) continue;
        if (ci(o.label) === ci(r.label)) block(r, { code: "label_dup", other: o.name });
        else if (o.awsName === r.awsName) block(r, { code: "name_collision", other: o.label });
      }
      for (const x of ex) {
        if (awsProfileName(x.label) === r.awsName) block(r, { code: "name_collision", other: x.label });
      }
    }
    if (!grew) break;
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
    logins: created.length,
    laptopSignIns: new Set(created.map((r) => r.signIn)).size,
  };
}

/** The rows to create, in the planner's shape. */
export function toImportSection(preview: ImportPreview): SsmSection {
  return { profiles: preview.rows.filter((r) => r.status === "create").map((r) => r.entry!), hosts: [] };
}
