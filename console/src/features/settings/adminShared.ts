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
  // How long a home may sit unopened before it is put away as a snapshot (ecs-ec2 only;
  // "" = deploy default, "0" = never). ADR 0045 決定 13-2.
  home_hibernate_after?: string;
  // How often to keep a copy of a home outside its AZ — the tenant's RPO (ecs-ec2 only;
  // "" = deploy default, "0" = no backups). ADR 0045 決定 17.
  home_backup_every?: string;
  allow_agent_self_update?: boolean;
  terminal_history_retention_days?: number;
  // Per-tenant login rules, stored as CSV (docs/61 §61.9.7).
  allowed_providers?: string;
  auto_join_domains?: string;
  allowed_domains?: string;
  // 受け入れるが、そのテナントのログイン画面には出さない方式（docs/61 §61.15.9）。
  hidden_providers?: string;
}

export interface Member {
  user_key: string;
  email?: string;
  role: string;
  super_admin?: boolean;
  state?: string;
  max_sessions?: number | null;
  mem_limit?: number | null; // per-workspace RAM cap in bytes (0/undefined = unset)
  cpu_limit?: number | null; // per-workspace CPU cap in Fargate units, 1024 = 1 vCPU (0/undefined = unset)
  disk_gb?: number | null; // per-workspace working disk in GiB (0/undefined = unset → 20 GiB free default)
  /** "active" | "removed". A removed member is off the roster and can no longer
   *  sign in, but stays on THIS list so the rest of the offboarding sequence
   *  (stop workspace → clean home) is still reachable (docs/61 §61.10.6). */
  status?: string;
}

/** What the three size axes MEAN on this deployment's runtime
 *  (GET /api/admin/workspace-sizing, ADR 0045 決定 21).
 *
 *  The stored shape is the same everywhere — three independent numbers — but what a
 *  stored number BECOMES is not. On the EC2 slot pool the CPU axis never reaches the
 *  backend, memory picks a box instead of capping one, and the disk number sizes the
 *  PERSISTENT home. The UI reads this instead of describing every deployment as if it
 *  were Fargate, which is what it used to do (docs/64 §64.27). */
export interface WsSizing {
  runtime: string;
  cpu_effective: boolean;
  /** "limit" = a cap (docker/Fargate) · "slot" = a requirement that picks a box. */
  mem_meaning: "limit" | "slot";
  /** "work" = wiped on stop · "home" = the persistent home · "quota" = display only. */
  disk_meaning: "work" | "home" | "quota";
  disk_default_gb?: number;
  disk_create_only?: boolean;
  slots?: WsSlot[];
}

export interface WsSlot {
  instance_type: string;
  mem_mib: number;
  /** 0/absent when the operator did not declare it — then no vCPU count is shown. */
  vcpu?: number;
}

/** The runtime's own answer, used while the profile is still loading. It matches the
 *  docker/Fargate description the UI has always shown, so nothing flickers into view. */
export const WS_SIZING_FALLBACK: WsSizing = {
  runtime: "",
  cpu_effective: true,
  mem_meaning: "limit",
  disk_meaning: "work",
};

/** The slot a memory request (in MiB) lands on: the smallest rung that holds it, and
 *  the top rung when nothing does — the same rule as the CP's slotTypeFor, including
 *  0 landing on the SMALLEST slot (on Fargate/docker 0 means the deployment default
 *  instead, which is why the caller must not reuse this for those runtimes). */
export function slotFor(slots: WsSlot[] | undefined, memMib: number): WsSlot | null {
  if (!slots || slots.length === 0) return null;
  return slots.find((s) => memMib <= s.mem_mib) ?? slots[slots.length - 1];
}

/** The workspace sizes offered as named choices. The three axes are stored as
 *  independent numbers (ADR 0044 決定 1); these presets exist only so an admin picks
 *  from combinations Fargate actually accepts instead of discovering the matrix by
 *  trial and error. cpu is in Fargate units (1024 = 1 vCPU), mem in MiB, disk in GiB.
 *  Every pair here is a measured-valid Fargate size (docs/63 §63.2). */
export const WS_SIZE_PRESETS = [
  { id: "s", label: "S", cpu: 1024, mem: 2048, disk: 0 },
  { id: "m", label: "M", cpu: 1024, mem: 4096, disk: 0 },
  { id: "l", label: "L", cpu: 2048, mem: 8192, disk: 40 },
  { id: "xl", label: "XL", cpu: 4096, mem: 16384, disk: 80 },
  { id: "2xl", label: "2XL", cpu: 8192, mem: 32768, disk: 160 },
] as const;

// GiB with adaptive precision (shared fmtGiB) plus AdminTab's "G" suffix.
export const fmtG = (b: number) => fmtGiB(b) + "G";
export const fmtPct = (n: number | null | undefined) => (n == null ? "–" : Math.round(n) + "%");
// MB → a "N GiB" hint for the memory input (whole number when clean, else 1 decimal).
export const fmtGbHint = (mb: number) => {
  const gb = mb / 1024;
  return (Number.isInteger(gb) ? String(gb) : gb.toFixed(1)) + " GiB";
};
