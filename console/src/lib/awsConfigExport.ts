// awsConfigExport — renders the member's Settings AWS profiles as ~/.aws/config text
// (issue #3017, part 3).
//
// It is "what Agent Fleet can express", not a reverse of an imported file: no static keys, MFA,
// credential_process or [default] come out, and the result is meant to be merged by hand, never
// to replace a member's file.
//
// Why a renderer and not string concatenation of the stored values: the CP checks an SSO
// profile's fields more loosely than the Agent does (a start URL only has to be https://\S+), so
// a stored value may hold what an INI reader takes for a new key or section. Every value is
// therefore checked here against the Agent's own allowlists (workspace/agent/internal/agents/ssm/
// config.go) before it is written, and the finished text is checked line by line once more. A
// row that fails is left out and reported with a reason that names the field, never its value.
//
// The rules about WHICH rows go out are the Agent's, so the file holds what af-aws-exec sees:
// a name that two labels share is left out of both (aws_profiles_bridge.go), `default` is
// reserved, an SSO row needs both an account and a role (awsx IncompleteReason), and a role row
// needs a source that is itself exported.
//
// Pure: no fetch, no React.

import { awsProfileName, validDuration, validExternalId, validRoleArn, validSessionName } from "./awsRoleChain.ts";

// The Agent's allowlists. Anchored and without \s, so a multi-line value never passes.
const RE = {
  startUrl: /^https:\/\/[!-~]+$/,
  region: /^[a-z0-9-]{1,32}$/,
  account: /^[0-9]{1,20}$/,
  role: /^[A-Za-z0-9+=,.@_-]{1,64}$/,
  profile: /^[A-Za-z0-9._@-]{1,64}$/,
};

export type ExportReason =
  | { code: "name_default" }
  | { code: "name_invalid" }
  | { code: "name_collision"; other: string }
  | { code: "incomplete"; missing: string[] }
  | { code: "invalid"; field: string }
  | { code: "source_missing" }
  | { code: "source_not_sso" }
  | { code: "source_excluded"; source: string };

export interface ExportedRow {
  label: string;
  /** The profile name in the file. */
  name: string;
  kind: "sso" | "assume_role";
}

export interface ExcludedRow {
  label: string;
  reason: ExportReason;
}

export interface AwsConfigExport {
  text: string;
  included: ExportedRow[];
  excluded: ExcludedRow[];
}

/** The download's name. Not `config`: this is a fragment to merge, and a file that looks like
 *  the real one gets copied over it. */
export const EXPORT_FILENAME = "agent-fleet-aws-profiles.aws-config-fragment.txt";

/** The sso-session a profile signs in through. One place decides it, so sign-ins that several
 *  profiles share (ADR 0125) change this function and nothing else: sessions are emitted once
 *  per name, in the order their first profile appears. Today it is the Agent's per-profile
 *  `af-<profile>`, which keeps the exported file's login cache key equal to the workspace's. */
function sessionOf(name: string, startUrl: string, ssoRegion: string): { name: string; startUrl: string; ssoRegion: string } {
  return { name: "af-" + name, startUrl, ssoRegion };
}

interface Prepared {
  row: any;
  label: string;
  name: string;
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** Check the fields of an SSO row; the first failing field's name (never its value), or null. */
function ssoProblem(p: any): ExportReason | null {
  const missing = [
    ["accountId", str(p.accountId)],
    ["roleName", str(p.roleName)],
  ]
    .filter(([, v]) => !v)
    .map(([k]) => k);
  if (missing.length) return { code: "incomplete", missing };
  for (const [field, v, re] of [
    ["startUrl", str(p.startUrl), RE.startUrl],
    ["ssoRegion", str(p.ssoRegion), RE.region],
    ["accountId", str(p.accountId), RE.account],
    ["roleName", str(p.roleName), RE.role],
  ] as const) {
    if (!re.test(v)) return { code: "invalid", field };
  }
  if (str(p.region) && !RE.region.test(str(p.region))) return { code: "invalid", field: "region" };
  return null;
}

function roleProblem(p: any): ExportReason | null {
  if (!validRoleArn(str(p.roleArn))) return { code: "invalid", field: "roleArn" };
  if (!validExternalId(str(p.externalId))) return { code: "invalid", field: "externalId" };
  if (!validSessionName(str(p.sessionName))) return { code: "invalid", field: "sessionName" };
  const d = p.durationSeconds;
  if (d !== undefined && d !== null && d !== 0 && !validDuration(Number(d))) return { code: "invalid", field: "durationSeconds" };
  if (str(p.region) && !RE.region.test(str(p.region))) return { code: "invalid", field: "region" };
  return null;
}

/** Render the rows of GET api/ssm/profiles. */
export function renderAwsConfig(rows: any[]): AwsConfigExport {
  const list: Prepared[] = (rows || []).map((row) => {
    const label = str(row?.label).trim();
    return { row, label, name: awsProfileName(label) };
  });
  const byName = new Map<string, Prepared[]>();
  for (const p of list) byName.set(p.name, [...(byName.get(p.name) || []), p]);
  const byId = new Map(list.map((p) => [str(p.row?.id), p]));

  const excluded = new Map<Prepared, ExportReason>();
  for (const p of list) {
    const same = byName.get(p.name)!;
    if (same.length > 1) excluded.set(p, { code: "name_collision", other: same.find((o) => o !== p)!.label });
    else if (p.name === "default") excluded.set(p, { code: "name_default" });
    else if (!RE.profile.test(p.name)) excluded.set(p, { code: "name_invalid" });
  }
  const isRole = (p: Prepared) => p.row?.kind === "assume_role";
  // SSO rows first: a role row is judged by what became of its source.
  for (const p of list) if (!excluded.has(p) && !isRole(p)) {
    const bad = ssoProblem(p.row);
    if (bad) excluded.set(p, bad);
  }
  for (const p of list) {
    if (excluded.has(p) || !isRole(p)) continue;
    const src = byId.get(str(p.row.sourceProfileId));
    let bad: ExportReason | null = null;
    if (!src) bad = { code: "source_missing" };
    else if (isRole(src)) bad = { code: "source_not_sso" };
    else if (excluded.has(src)) bad = { code: "source_excluded", source: src.label };
    else bad = roleProblem(p.row);
    if (bad) excluded.set(p, bad);
  }

  const out: string[] = [
    "# Exported from Agent Fleet (Settings > AWS profiles/SSM).",
    "# This is a fragment to merge into ~/.aws/config by hand, not a replacement for it:",
    "# it holds only the profiles Agent Fleet can express, and none of your keys, MFA or",
    "# command-credential profiles. Each profile signs in through its own sso-session.",
  ];
  const sessions = new Set<string>();
  const included: ExportedRow[] = [];
  const put = (s: string) => out.push(s);
  for (const p of list) {
    if (excluded.has(p) || isRole(p)) continue;
    const r = p.row;
    const s = sessionOf(p.name, str(r.startUrl), str(r.ssoRegion));
    if (!sessions.has(s.name)) {
      sessions.add(s.name);
      put("");
      put(`[sso-session ${s.name}]`);
      put(`sso_start_url = ${s.startUrl}`);
      put(`sso_region = ${s.ssoRegion}`);
      put("sso_registration_scopes = sso:account:access");
    }
    put("");
    put(`[profile ${p.name}]`);
    put(`sso_session = ${s.name}`);
    put(`sso_account_id = ${str(r.accountId)}`);
    put(`sso_role_name = ${str(r.roleName)}`);
    // As the Agent writes it: without a region of its own, the SSO region.
    put(`region = ${str(r.region) || str(r.ssoRegion)}`);
    included.push({ label: p.label, name: p.name, kind: "sso" });
  }
  for (const p of list) {
    if (excluded.has(p) || !isRole(p)) continue;
    const r = p.row;
    const src = byId.get(str(r.sourceProfileId))!;
    put("");
    put(`[profile ${p.name}]`);
    put(`role_arn = ${str(r.roleArn)}`);
    put(`source_profile = ${src.name}`);
    if (str(r.externalId)) put(`external_id = ${str(r.externalId)}`);
    if (str(r.sessionName)) put(`role_session_name = ${str(r.sessionName)}`);
    if (r.durationSeconds) put(`duration_seconds = ${Number(r.durationSeconds)}`);
    put(`region = ${str(r.region) || str(src.row.ssoRegion)}`);
    included.push({ label: p.label, name: p.name, kind: "assume_role" });
  }
  const text = out.join("\n") + "\n";
  assertSafe(text);
  return {
    text,
    included,
    excluded: list.filter((p) => excluded.has(p)).map((p) => ({ label: p.label, reason: excluded.get(p)! })),
  };
}

// The only line shapes the renderer writes. A value that slipped past the allowlists above and
// holds a newline, `[` or `#` would break one of them, so nothing it carried can be a key or a
// section of its own. Throws rather than returning text: a caller must not be able to download it.
const SAFE_LINE = [
  /^$/,
  /^# [^\r\n]*$/,
  /^\[(?:sso-session|profile) [A-Za-z0-9._@-]{1,67}\]$/,
  /^(?:sso_start_url|sso_region|sso_registration_scopes|sso_session|sso_account_id|sso_role_name|region|role_arn|source_profile|external_id|role_session_name|duration_seconds) = [^\s\[#;][^\r\n]*$/,
];
export function assertSafe(text: string): void {
  for (const line of text.replace(/\n$/, "").split("\n")) {
    if (!SAFE_LINE.some((re) => re.test(line))) throw new Error("awsConfigExport: unsafe line");
  }
}
