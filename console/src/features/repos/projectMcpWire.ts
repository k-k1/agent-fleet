// The wire contract for docs/56 P0's read-only project-scope MCP snapshot
// (workspace/agent/internal/mcpproj, GET /api/repos/{name}/mcp). Kept separate from
// ../settings/mcpWire.ts (the AF REGISTRY's wire contract) on purpose: ADR0040 決定15
// keeps the Go types apart too — a project file's "af" entry must be FOUND and
// flagged here, not treated like an editable registry row — so this file defines
// its own interfaces rather than reusing/extending McpServer/Registry.
//
// mcpproj never sends prose for a warning (docs/23 P0-3's one-code-per-reason rule):
// every Warning is a code plus parameters, and warningText() below is where the
// Console turns that back into a localized sentence.

import { api, apiJSON } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";

export interface ProjectServer {
  name: string;
  transport: "stdio" | "http";
  command?: string;
  args?: string[];
  env?: Record<string, string>; // values masked ("***") or "" (unset)
  url?: string;
  headers?: Record<string, string>;
  extra?: Record<string, unknown>;
}

export interface ProjectFile {
  path: string;
  kinds: string[];
  exists: boolean;
  parsable: boolean;
  tracked: boolean;
  trackedUncertain?: boolean;
  ignored: boolean;
  servers?: ProjectServer[];
  note?: string;
}

export interface ProjectKindInfo {
  kind: string;
  hasProjectScope: boolean;
  unverified?: boolean;
  gateCode?: "approval" | "trust" | "none" | "";
  dialects?: string[];
}

export interface ProjectWarning {
  severity: "red" | "yellow";
  code: string;
  file?: string;
  files?: string[];
  server?: string;
  key?: string;
  kind?: string;
  dialect?: string;
}

export interface ProjectSnapshot {
  repo: string;
  vcs: "git" | "svn" | "none";
  worktree: boolean;
  files: ProjectFile[];
  kinds: ProjectKindInfo[];
  warnings?: ProjectWarning[];
}

export const fetchProjectMcpSnapshot = (repo: string): Promise<ProjectSnapshot> =>
  api(`api/repos/${encodeURIComponent(repo)}/mcp`);

// --- P1: plan → apply (docs/56 §5 / §10) --------------------------------------
//
// A pure ops list, computed client-side from the panel's form state — never
// stored server-side (docs/56 §5's "純粋なワンショット"). planHash is an opaque
// echo: compute it via planProjectMcp, pass the SAME value to applyProjectMcp: a
// 409 means a file the ops would write changed in between (docs/56 §5's
// optimistic lock) and the panel must re-plan rather than retry blindly.

export type OnConflict = "overwrite" | "skip" | "rename";
export type DialectChoice = "as-is" | "translate" | "expand";
export type IgnoreWhere = "exclude" | "gitignore";

export interface ProjectOp {
  op: "copy" | "ignore";
  // copy
  from?: { file: string; name: string };
  to?: { file: string };
  as?: string;
  onConflict?: OnConflict;
  withSecrets?: boolean;
  dialect?: DialectChoice;
  // ignore
  file?: string;
  where?: IgnoreWhere;
}

export interface ProjectOpResult {
  index: number;
  status: "ok" | "skipped" | "error";
  reason?: string;
  file?: string;
  resolvedName?: string;
  before?: ProjectServer;
  after?: ProjectServer;
  gateCode?: string;
  ignoreFile?: string;
  alreadyPresent?: boolean;
}

export interface ProjectPlanResult {
  planHash: string;
  ops: ProjectOpResult[];
  warnings?: ProjectWarning[];
}

export const planProjectMcp = (repo: string, ops: ProjectOp[]): Promise<ProjectPlanResult> =>
  apiJSON(`api/repos/${encodeURIComponent(repo)}/mcp/plan`, "POST", { ops });

export const applyProjectMcp = (repo: string, ops: ProjectOp[], planHash: string): Promise<ProjectPlanResult> =>
  apiJSON(`api/repos/${encodeURIComponent(repo)}/mcp/apply`, "POST", { ops, planHash });

/** Whether file already has an entry named name — for progressive disclosure of
 * the onConflict choice (docs/56 §9.2: only ask when there IS a conflict). */
export function targetHasEntry(files: ProjectFile[], file: string, name: string): boolean {
  const f = files.find((x) => x.path === file);
  return !!f?.servers?.some((s) => s.name === name);
}

export function opErrorText(reason?: string): string {
  switch (reason) {
    case "mcp_project_copy_source_unreadable":
      return t("pmcp.op_source_unreadable");
    case "mcp_project_copy_source_missing":
      return t("pmcp.op_source_missing");
    case "mcp_project_copy_dest_unreadable":
      return t("pmcp.op_dest_unreadable");
    case "mcp_project_copy_conflict":
      return t("pmcp.op_conflict");
    default:
      return reason || t("err.unknown");
  }
}

// --- matrix helpers (servers × files, docs/56 §9.2 ②) -----------------------

/** Every distinct server name across every file, sorted — the matrix rows. */
export function matrixServerNames(files: ProjectFile[]): string[] {
  const names = new Set<string>();
  for (const f of files) for (const s of f.servers || []) names.add(s.name);
  return [...names].sort();
}

export function serverIn(file: ProjectFile, name: string): ProjectServer | undefined {
  return (file.servers || []).find((s) => s.name === name);
}

/** Every server name any CodeServerDiverged warning names, for the ⚠差分 cell mark. */
export function divergedNames(warnings: ProjectWarning[] | undefined): Set<string> {
  const out = new Set<string>();
  for (const w of warnings || []) if (w.code === "mcp_project_server_diverged" && w.server) out.add(w.server);
  return out;
}

// --- localized text -----------------------------------------------------------

function dialectLabel(d?: string): string {
  switch (d) {
    case "dollar_brace":
      return "${VAR}";
    case "dollar_env_brace":
      return "${env:VAR}";
    case "env_brace":
      return "{env:VAR}";
    default:
      return d || "";
  }
}

export function dialectsText(dialects?: string[]): string {
  if (!dialects || dialects.length === 0) return t("pmcp.dialect_none");
  return dialects.map(dialectLabel).join(" / ");
}

export function gateText(code: string | undefined): string | undefined {
  switch (code) {
    case "approval":
      return t("pmcp.gate_approval");
    case "trust":
      return t("pmcp.gate_trust");
    case "none":
      return t("pmcp.gate_none");
    default:
      return undefined;
  }
}

export function warningText(w: ProjectWarning): string {
  switch (w.code) {
    case "mcp_project_file_unreadable":
      return t("pmcp.w_file_unreadable", { file: w.file || "" });
    case "mcp_project_name_hijack":
      return t("pmcp.w_name_hijack", { server: w.server || "", file: w.file || "" });
    case "mcp_project_name_invalid":
      return t("pmcp.w_name_invalid", { server: w.server || "", file: w.file || "" });
    case "mcp_project_dialect_broken":
      return t("pmcp.w_dialect_broken", {
        server: w.server || "",
        file: w.file || "",
        kind: w.kind || "",
        dialect: dialectLabel(w.dialect),
      });
    case "mcp_project_dialect_mismatch":
      return t("pmcp.w_dialect_mismatch", {
        server: w.server || "",
        file: w.file || "",
        kind: w.kind || "",
        dialect: dialectLabel(w.dialect),
      });
    case "mcp_project_secret_tracked":
      return t("pmcp.w_secret_tracked", { server: w.server || "", file: w.file || "", key: w.key || "" });
    case "mcp_project_secret_vcs_uncertain":
      return t("pmcp.w_secret_vcs_uncertain", { server: w.server || "", file: w.file || "", key: w.key || "" });
    case "mcp_project_server_diverged":
      return t("pmcp.w_server_diverged", { server: w.server || "", files: (w.files || []).join(", ") });
    default:
      return w.code;
  }
}
