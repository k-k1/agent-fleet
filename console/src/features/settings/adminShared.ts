// 管理系の面を「読み手ごとのモーダル」へ分けたときに、両側から要るものだけを置く場所。
//
// AdminTab（デプロイ管理者）と TenantDialog（テナント管理者）は同じ CP の管理 API を
// 読む。だから API の形（Tenant / Member）と、その数値の見せ方（GiB・%）は 1 つで
// なければならない — 2 つに写すと、片方だけ直る類のズレが必ず出る。
//
// ★ ここには権限の判断を置かない。サーバが持っているものを UI に写す道具だけ。
import { fmtGiB } from "../../lib/bytes.ts";

// Admin API shapes (only the fields the UI reads; server responses may carry more).
export interface Tenant {
  slug: string;
  name: string;
  users?: number;
  running?: number;
  max_workspaces?: number;
  max_sessions?: number;
  max_git_repos?: number;
  max_lfs_bytes?: number;
  max_workspace_mem?: number; // per-workspace RAM cap in bytes (0 = no tenant cap)
  session_idle_timeout?: string;
  ws_idle_timeout?: string;
  allow_agent_self_update?: boolean;
  terminal_history_retention_days?: number;
  // Per-tenant login rules, stored as CSV (docs/61 §61.9.7).
  allowed_providers?: string;
  auto_join_domains?: string;
  allowed_domains?: string;
}

export interface Member {
  user_key: string;
  email?: string;
  role: string;
  super_admin?: boolean;
  state?: string;
  max_sessions?: number | null;
  mem_limit?: number | null; // per-workspace RAM cap in bytes (0/undefined = unset)
  /** "active" | "removed". A removed member is off the roster and can no longer
   *  sign in, but stays on THIS list so the rest of the offboarding sequence
   *  (stop workspace → clean home) is still reachable (docs/61 §61.10.6). */
  status?: string;
}

// GiB with adaptive precision (shared fmtGiB) plus AdminTab's "G" suffix.
export const fmtG = (b: number) => fmtGiB(b) + "G";
export const fmtPct = (n: number | null | undefined) => (n == null ? "–" : Math.round(n) + "%");
// MB → a "N GiB" hint for the memory input (whole number when clean, else 1 decimal).
export const fmtGbHint = (mb: number) => {
  const gb = mb / 1024;
  return (Number.isInteger(gb) ? String(gb) : gb.toFixed(1)) + " GiB";
};
