// sshConfigImport — turns the SSM entries of a member's ~/.ssh/config into Settings SSM hosts
// (issue #3017, part 1; the AWS profile side is awsConfigImport.ts, #3010).
//
//   sshConfigIni.parseSshConfig -> readSshConfig (what each Host name is) -> resolveSshImport
//   (against the member's Settings profiles / hosts and the preview's edits) -> toSshImportSection
//   -> planSsmImport -> executeSsmPlan
//
// What is importable, exactly. The usual entry is
//
//     Host web
//       HostName i-0123456789abcdef0
//       ProxyCommand aws ssm start-session --target %h --document-name AWS-StartSSHSession --parameters portNumber=%p
//
// which is an SSH session carried over an SSM tunnel (user, key, port), while an Agent Fleet SSM
// host is an `aws ssm start-session` shell with none of those. Turning one into the other
// changes how the member connects; the preview says so on every row. Only this static form is
// read, and anything else is refused with a reason:
//
//   - A concrete `Host` name (no `*`, `?`, `!`). A pattern, a negation, a `Match` block and an
//     `Include` line are rows of their own, never importable.
//   - OpenSSH is first-match-wins per keyword across every block that applies to the name, so
//     blocks are not independent records. The ProxyCommand (and the HostName, when `%h` needs it)
//     must be the first value for that name AND come from a block that names it exactly: a value
//     inherited from `Host *` or an earlier pattern is refused, and so is any value that follows
//     a `Match` that sets one of these keywords, or an `Include` (its content is not in the
//     text), which could have supplied it first.
//   - The command is exactly `aws ssm start-session`, optionally wrapped as `sh -c "…"` (the form
//     AWS documents), with only --target, --document-name AWS-StartSSHSession, --parameters
//     portNumber=%p (or =22), --profile and --region, each once. `--target` is an instance id, or
//     `%h`, which is the HostName's value (else the Host name itself) and must be an instance id.
//     Any other document (port forwarding, custom), option, `%` token, shell metacharacter or
//     quoting is refused.
//   - The command is NEVER executed, and is not kept: the reader keeps only the validated
//     instance id, profile name and region. A reason names no part of it.
//
// The AWS profile is the one named by --profile, matched to a Settings profile by the AWS name
// its label becomes (awsProfileName); when the command names none, or none matches, the member
// picks one in the preview.
//
// Pure: no fetch, no React.

import { hasSshWildcard, sshGlob, type SshFile, type SshLine } from "./sshConfigIni.ts";
import { RE, SECRET_KEYS, shownName } from "./awsConfigImport.ts";
import { awsProfileName } from "./awsRoleChain.ts";
import type { SsmHostEntry, SsmSection } from "./settingsBundle.ts";

/** Above this many rows the preview stops being reviewable; the file is refused whole. */
export const MAX_ENTRIES = 500;

export const INSTANCE_RE = /^(?:i|mi)-[0-9a-f]{8,17}$/;
const SSH_DOC = "AWS-StartSSHSession";
// A word of the command, after its quotes are taken off. No shell metacharacter, no path, no
// `$`, so nothing here can mean anything but itself.
const WORD_OK = /^[A-Za-z0-9._@%=,-]+$/;
const ALIAS_RE = /^[A-Za-z0-9._@-]{1,64}$/;
// Keywords the preview may name as "not used by Settings" (a key name from the file is file
// text too: only these are shown).
const IGNORED_KNOWN = new Set([
  "user",
  "port",
  "identityfile",
  "identitiesonly",
  "forwardagent",
  "localforward",
  "remoteforward",
  "dynamicforward",
  "serveraliveinterval",
  "serveralivecountmax",
  "stricthostkeychecking",
  "userknownhostsfile",
  "addkeystoagent",
  "usekeychain",
  "compression",
  "requesttty",
  "sendenv",
  "setenv",
]);
// Keywords whose first value decides what a name connects to. A Match block that sets one, or
// an Include, may have supplied it before the line this reader found.
const DECIDING = new Set(["proxycommand", "proxyjump", "hostname", "canonicalizehostname", "include"]);

export type SshReason =
  | { code: "wildcard" }
  | { code: "negation" }
  | { code: "match" }
  | { code: "include" }
  | { code: "not_ssm" }
  | { code: "barrier" }
  | { code: "inherited"; key: string }
  | { code: "proxy_jump" }
  | { code: "canonical" }
  | { code: "other_command" }
  | { code: "not_static" }
  | { code: "unsupported_option" }
  | { code: "port_forward_doc" }
  | { code: "doc_other" }
  | { code: "no_document" }
  | { code: "parameters" }
  | { code: "no_target" }
  | { code: "unresolved" }
  | { code: "target_not_instance" }
  | { code: "invalid"; key: string }
  | { code: "alias_empty" }
  | { code: "alias_invalid" }
  | { code: "alias_taken"; other: string }
  | { code: "alias_dup"; other: string }
  | { code: "profile_unset" }
  | { code: "profile_missing"; name: string }
  | { code: "profile_ambiguous"; name: string }
  | { code: "profile_label_dup" };

type Proxy = { ok: true; target: string; profile: string; region: string } | { ok: false; reason: SshReason };

/** Words of the command, each whole-quoted or bare; null on any other quoting. */
function shellWords(s: string): string[] | null {
  const out: string[] = [];
  let rest = s.trim();
  while (rest) {
    const m = /^(?:'([^']*)'|"([^"]*)"|([^\s'"]+))(?=\s|$)/.exec(rest);
    if (!m) return null;
    out.push(m[1] ?? m[2] ?? m[3]);
    rest = rest.slice(m[0].length).trim();
  }
  return out;
}

/** Read one ProxyCommand value. `target` is an instance id or the literal "%h". */
function parseProxy(args: string): Proxy {
  const fail = (code: SshReason["code"]): Proxy => ({ ok: false, reason: { code } as SshReason });
  let inner = args.trim();
  // `sh -c "aws …"`, as the AWS documentation writes it (the inner command may use single quotes).
  const wrap = /^sh\s+-c\s+(?:"([^"]*)"|'([^']*)')$/.exec(inner);
  if (wrap) inner = wrap[1] ?? wrap[2];
  const w = shellWords(inner);
  if (!w) return fail("not_static");
  if (w.some((x) => !WORD_OK.test(x))) return fail(/^[A-Za-z0-9._-]+$/.test(w[0] ?? "") ? "not_static" : "other_command");
  if (w[0] !== "aws" || w[1] !== "ssm" || w[2] !== "start-session") return fail("other_command");
  const opt = new Map<string, string>();
  for (let i = 3; i < w.length; i++) {
    const tok = w[i];
    if (!tok.startsWith("--")) return fail("not_static");
    const eq = tok.indexOf("=");
    const name = eq >= 0 ? tok.slice(2, eq) : tok.slice(2);
    const value = eq >= 0 ? tok.slice(eq + 1) : w[++i];
    if (value === undefined || opt.has(name)) return fail("not_static");
    if (!["target", "document-name", "parameters", "profile", "region"].includes(name)) return fail("unsupported_option");
    opt.set(name, value);
  }
  const doc = opt.get("document-name");
  if (doc === undefined) return fail("no_document");
  if (doc !== SSH_DOC) return fail(/forward|tunnel/i.test(doc) ? "port_forward_doc" : "doc_other");
  const params = opt.get("parameters");
  if (params !== undefined && params !== "portNumber=%p" && params !== "portNumber=22") return fail("parameters");
  const target = opt.get("target");
  if (target === undefined) return fail("no_target");
  if (target !== "%h" && !INSTANCE_RE.test(target)) return fail(target.includes("%") ? "unresolved" : "target_not_instance");
  const profile = opt.get("profile") ?? "";
  if (profile && (!RE.profile.test(profile) || !shownName(profile))) return { ok: false, reason: { code: "invalid", key: "--profile" } };
  const region = opt.get("region") ?? "";
  if (region && !RE.region.test(region)) return { ok: false, reason: { code: "invalid", key: "--region" } };
  return { ok: true, target, profile, region };
}

/** One row of the preview before it meets the member's Settings. */
export interface SshEntry {
  /** Row identity: the lower-cased host name, or the line for a row that is not a host. */
  id: string;
  line: number;
  kind: "host" | "pattern" | "match" | "include";
  /** The Host name; "" for a row that is not a concrete host. */
  name: string;
  /** `name` when it is safe to show and to propose as an alias, else "" (the row is then shown
   *  by its line and needs an alias typed in). */
  display: string;
  reason: SshReason | null;
  instanceId: string;
  /** The profile name the command gave ("" = none). */
  profile: string;
  region: string;
  /** Keywords this host sets that an SSM shell does not use (known names only). */
  ignored: string[];
}

export interface NativeSsh {
  entries: SshEntry[];
  /** The text holds something that looks like a key or token. Not used, only warned about. */
  secrets: boolean;
  bomStripped: boolean;
  tooMany: boolean;
}

interface Hit extends SshLine {
  /** The block names this host exactly (not through a pattern or the global lines). */
  own: boolean;
  /** A Match that sets a deciding keyword, or an Include, came before this line. */
  afterBarrier: boolean;
}

/** The lines of every block that applies to `name`, in the order OpenSSH reads them. */
function applicable(file: SshFile, name: string): { hits: Hit[]; barrier: boolean } {
  const hits: Hit[] = [];
  let barrier = false;
  for (const b of file.blocks) {
    let own = false;
    if (b.kind === "match") {
      // Whether it applies is not decided here; it matters only if it could set a deciding value.
      if (b.lines.some((l) => DECIDING.has(l.key))) barrier = true;
      continue;
    }
    if (b.kind === "host") {
      const pos = b.patterns.filter((p) => !p.startsWith("!"));
      const negated = b.patterns.some((p) => p.startsWith("!") && sshGlob(p.slice(1), name));
      if (negated || !pos.some((p) => sshGlob(p, name))) continue;
      own = pos.some((p) => !hasSshWildcard(p) && p === name);
    }
    for (const l of b.lines) {
      if (l.key === "include") barrier = true;
      hits.push({ ...l, own, afterBarrier: barrier });
    }
  }
  return { hits, barrier };
}

function evaluate(file: SshFile, name: string): Pick<SshEntry, "reason" | "instanceId" | "profile" | "region" | "ignored"> {
  const none = { instanceId: "", profile: "", region: "", ignored: [] as string[] };
  const { hits, barrier } = applicable(file, name);
  const first = (k: string) => hits.find((h) => h.key === k);
  const pc = first("proxycommand");
  if (!pc) return { ...none, reason: { code: barrier ? "barrier" : "not_ssm" } };
  if (pc.afterBarrier) return { ...none, reason: { code: "barrier" } };
  if (!pc.own) return { ...none, reason: { code: "inherited", key: "ProxyCommand" } };
  if (pc.args.trim().toLowerCase() === "none") return { ...none, reason: { code: "not_ssm" } };
  const pj = first("proxyjump");
  if (pj && pj.args.trim().toLowerCase() !== "none") return { ...none, reason: { code: "proxy_jump" } };
  const ch = first("canonicalizehostname");
  if (ch && ch.args.trim().toLowerCase() !== "no") return { ...none, reason: { code: "canonical" } };
  const p = parseProxy(pc.args);
  if (!p.ok) return { ...none, reason: p.reason };
  let instanceId = p.target;
  if (p.target === "%h") {
    // %h is the HostName after its own expansion, else the name as typed.
    const hn = first("hostname");
    let host = name;
    // Without a HostName here, a later Match or Include could still supply one.
    if (!hn && barrier) return { ...none, reason: { code: "barrier" } };
    if (hn) {
      if (hn.afterBarrier) return { ...none, reason: { code: "barrier" } };
      if (!hn.own) return { ...none, reason: { code: "inherited", key: "HostName" } };
      host = hn.args.trim().replace(/^"([^"]*)"$/, "$1");
      if (host.includes("%")) return { ...none, reason: { code: "unresolved" } };
    }
    if (!INSTANCE_RE.test(host)) return { ...none, reason: { code: "target_not_instance" } };
    instanceId = host;
  }
  const ignored = [...new Set(hits.map((h) => h.key).filter((k) => IGNORED_KNOWN.has(k)))];
  return { reason: null, instanceId, profile: p.profile, region: p.region, ignored };
}

const shownAlias = (v: string): string => (ALIAS_RE.test(v) && shownName(v) ? v : "");

export function readSshConfig(file: SshFile): NativeSsh {
  let secrets = false;
  const entries: SshEntry[] = [];
  const seen = new Set<string>();
  const blank = { name: "", display: "", instanceId: "", profile: "", region: "", ignored: [] as string[] };
  const nonHost = (line: number, kind: SshEntry["kind"], reason: SshReason): SshEntry => ({
    ...blank,
    id: kind + ":" + line,
    line,
    kind,
    reason,
  });
  // Count before evaluating: each host walks every block, so the cap has to stop the work, not
  // just the result.
  let rowsUpper = 0;
  for (const b of file.blocks) {
    rowsUpper += b.kind === "host" ? b.patterns.length : b.kind === "match" ? 1 : 0;
    rowsUpper += b.lines.filter((l) => l.key === "include").length;
  }
  if (rowsUpper > MAX_ENTRIES) return { entries: [], secrets: false, bomStripped: file.bomStripped, tooMany: true };
  for (const b of file.blocks) {
    for (const l of b.lines) {
      const text = (l.key + " " + l.args).toLowerCase();
      if (SECRET_KEYS.some((s) => text.includes(s))) secrets = true;
      if (l.key === "include") entries.push(nonHost(l.line, "include", { code: "include" }));
    }
    if (b.kind === "match") entries.push(nonHost(b.line, "match", { code: "match" }));
    if (b.kind !== "host") continue;
    b.patterns.forEach((pat, i) => {
      if (pat.startsWith("!")) {
        entries.push({ ...nonHost(b.line, "pattern", { code: "negation" }), id: "pattern:" + b.line + "#" + i });
      } else if (hasSshWildcard(pat)) {
        entries.push({ ...nonHost(b.line, "pattern", { code: "wildcard" }), id: "pattern:" + b.line + "#" + i });
      } else if (!seen.has(pat)) {
        // A name listed in several blocks is one host to OpenSSH (names are case-sensitive): one
        // row, evaluated over all of them.
        seen.add(pat);
        entries.push({ ...blank, id: "host:" + pat, line: b.line, kind: "host", name: pat, display: shownAlias(pat), ...evaluate(file, pat) });
      }
    });
  }
  entries.sort((a, b) => a.line - b.line);
  return { entries, secrets, bomStripped: file.bomStripped, tooMany: entries.length > MAX_ENTRIES };
}

/** A member's change to one row in the preview. */
export interface SshEdit {
  selected?: boolean;
  alias?: string;
  /** The Settings profile chosen for a host the command did not (or could not) name. */
  profileId?: string;
}

export type SshStatus = "create" | "exists" | "blocked" | "unselected";

export interface SshRow extends SshEntry {
  alias: string;
  selected: boolean;
  status: SshStatus;
  /** The Settings profile the host will use (label), once resolved. */
  profileLabel?: string;
  /** How it was resolved: by the AWS name the command gave, or by the member's pick. */
  via?: "named" | "picked";
  entry?: SsmHostEntry;
}

export interface SshPreview {
  rows: SshRow[];
  profileChoices: { id: string; label: string; awsName: string }[];
  /** Rows that will be created or already exist as a shell where the file had an SSH tunnel. */
  converted: number;
}

const ci = (s: string) => s.trim().toLowerCase();

/** Resolve the entries against the member's Settings (the CP lists, as GET api/ssm/profiles and
 *  api/ssm/hosts return them) and the preview's edits. Re-run on every edit. */
export function resolveSshImport(cfg: NativeSsh, profiles: any[], hosts: any[], edits: Record<string, SshEdit>): SshPreview {
  const exP = (profiles || []).map((p) => ({ id: String(p?.id ?? ""), label: String(p?.label ?? "").trim() }));
  const exH = (hosts || []).map((h) => ({ alias: ci(String(h?.alias ?? "")), label: String(h?.alias ?? "").trim(), instanceId: ci(String(h?.instanceId ?? "")) }));
  const taken = new Map<string, string>(); // alias (folded) -> display of the row holding it
  const rows: SshRow[] = cfg.entries.map((e) => {
    const ed = edits[e.id] || {};
    const alias = (ed.alias ?? e.display).trim();
    const row: SshRow = { ...e, alias, selected: ed.selected ?? true, status: "blocked" };
    if (e.reason || e.kind !== "host") return row;
    const block = (reason: SshReason): SshRow => ({ ...row, reason });
    if (!alias) return block({ code: "alias_empty" });
    if (!ALIAS_RE.test(alias)) return block({ code: "alias_invalid" });
    // The profile: the member's pick, else the Settings profile whose AWS name is the one the
    // command named. Never by guessing from the host name.
    let prof = ed.profileId ? exP.find((p) => p.id === ed.profileId) : undefined;
    let via: SshRow["via"] = prof ? "picked" : undefined;
    if (!prof) {
      if (!e.profile) return block({ code: "profile_unset" });
      const hits = exP.filter((p) => awsProfileName(p.label) === e.profile);
      if (hits.length === 0) return block({ code: "profile_missing", name: e.profile });
      if (hits.length > 1) return block({ code: "profile_ambiguous", name: e.profile });
      prof = hits[0];
      via = "named";
    }
    // The executor binds a host to its profile by label; two rows sharing one cannot be told apart.
    if (exP.filter((p) => ci(p.label) === ci(prof!.label)).length > 1) return block({ code: "profile_label_dup" });
    const entry: SsmHostEntry = { alias, profile: prof.label, instanceId: e.instanceId, documentName: "", region: e.region };
    const same = exH.find((h) => h.alias === ci(alias) && h.instanceId === ci(e.instanceId));
    if (same) return { ...row, status: "exists", entry, profileLabel: prof.label, via };
    const other = exH.find((h) => h.alias === ci(alias));
    if (other) return block({ code: "alias_taken", other: other.label });
    return { ...row, entry, profileLabel: prof.label, via, status: row.selected ? "create" : "unselected" };
  });
  // Aliases among the creations: the first keeps it, a later one is refused.
  for (const r of rows) {
    if (r.status !== "create") continue;
    const k = ci(r.alias);
    const prev = taken.get(k);
    if (prev !== undefined) Object.assign(r, { status: "blocked", reason: { code: "alias_dup", other: prev } });
    else taken.set(k, r.display || r.alias);
  }
  return {
    rows,
    profileChoices: exP.map((p) => ({ id: p.id, label: p.label, awsName: awsProfileName(p.label) })),
    converted: rows.filter((r) => r.status === "create").length,
  };
}

/** The hosts to create, in the planner's shape. */
export function toSshImportSection(preview: SshPreview): SsmSection {
  return { profiles: [], hosts: preview.rows.filter((r) => r.status === "create").map((r) => r.entry!) };
}
