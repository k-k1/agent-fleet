// 管理系の面を「読み手ごとのモーダル」へ分けたときに、両側から要るものだけを置く場所。
//
// AdminTab（デプロイ管理者）と TenantDialog（テナント管理者）は同じ CP の管理 API を
// 読む。だから API の形（Tenant / Member）と、その数値の見せ方（GiB・%）は 1 つで
// なければならない — 2 つに写すと、片方だけ直る類のズレが必ず出る。
//
// ★ ここには権限の判断を置かない。サーバが持っているものを UI に写す道具だけ。
import { fmtGiB } from "../../lib/bytes.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";

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
  /** 人の判断待ち専用の tier1 タイムアウト（docs/log/75）。空 = session_idle_timeout に従う。 */
  interaction_idle_timeout?: string;
  // How long a home may sit unopened before it is put away as a snapshot (ecs-ec2 only;
  // "" = deploy default, "0" = never). ADR 0045 決定 13-2.
  home_hibernate_after?: string;
  // How often to keep a copy of a home outside its AZ — the tenant's RPO (ecs-ec2 only;
  // "" = deploy default, "0" = no backups). ADR 0045 決定 17.
  home_backup_every?: string;
  allow_agent_self_update?: boolean;
  terminal_history_retention_days?: number;
  // Per-tenant login rules, stored as CSV (docs/log/61 §61.9.7).
  allowed_providers?: string;
  auto_join_domains?: string;
  allowed_domains?: string;
  // 受け入れるが、そのテナントのログイン画面には出さない方式（docs/log/61 §61.15.9）。
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
  /** Which KIND of machine the workspace lands on, as a deployment-declared class id
   *  ("" / undefined = the tenant default). Not a size — mem_limit still picks the
   *  rung within the class (docs/log/70). */
  slot_class?: string | null;
  /** "active" | "removed". A removed member is off the roster and can no longer
   *  sign in, but stays on THIS list so the rest of the offboarding sequence
   *  (stop workspace → clean home) is still reachable (docs/log/61 §61.10.6). */
  status?: string;
  /** 自動停止の見通し（docs/log/75 P4）。reaper が最後に観測したもので、稼働中の
   *  Workspace にだけ入る。**画面はこれを再計算しない** — 自前で導出すると reaper が
   *  実際に見ているもの（在席・ピン・背景作業）とズレて、「なぜ止まらないのか」を
   *  調べるための画面が別の答えを出す。 */
  idle?: MemberIdle;
}

export interface MemberIdle {
  /** このテナントで tier2（Workspace 停止）が有効か。false は「予定なし」ではなく
   *  「機能が切ってある」— 設定ミスと区別できる必要がある。 */
  enabled: boolean;
  /** 今の観測が続いた場合の停止予定時刻（RFC3339）。holders が空のときだけ意味を持つ。 */
  stopAt?: string;
  /** 止めない理由。空 = 何も止めていない（stopAt に向かって数えている）。 */
  holders?: Array<{ kind: string; session?: string; until?: string }>;
  /** 観測時刻。スイープ間隔ぶん古いので、秒単位の断言をさせないために出す。 */
  observedAt: string;
}

/** What the three size axes MEAN on this deployment's runtime
 *  (GET /api/admin/workspace-sizing, ADR 0045 決定 21).
 *
 *  The stored shape is the same everywhere — three independent numbers — but what a
 *  stored number BECOMES is not. On the EC2 slot pool the CPU axis never reaches the
 *  backend, memory picks a box instead of capping one, and the disk number sizes the
 *  PERSISTENT home. The UI reads this instead of describing every deployment as if it
 *  were Fargate, which is what it used to do (docs/log/64 §64.27). */
export interface WsSizing {
  runtime: string;
  cpu_effective: boolean;
  /** "limit" = a cap (docker/Fargate) · "slot" = a requirement that picks a box. */
  mem_meaning: "limit" | "slot";
  /** "work" = wiped on stop · "home" = the persistent home · "quota" = display only. */
  disk_meaning: "work" | "home" | "quota";
  disk_default_gb?: number;
  disk_create_only?: boolean;
  /** The DEFAULT class's ladder when there is more than one class. */
  slots?: WsSlot[];
  /** The machine classes this deployment offers. Absent on a deployment that declared
   *  a single unnamed ladder — a picker with one entry is a question with one possible
   *  answer, so there is no picker at all (docs/log/70 §70.10). */
  slot_classes?: WsSlotClass[];
  default_slot_class?: string;
}

/** One declared machine class: the operator's own label, a CPU architecture and its
 *  own ladder. The LABEL is what the admin sees — they are choosing "省コスト（Arm）",
 *  not an EC2 instance family; the instance type appears only in the "you land on"
 *  line underneath. */
export interface WsSlotClass {
  id: string;
  label: string;
  arch: string;
  slots: WsSlot[];
}

export interface WsSlot {
  instance_type: string;
  mem_mib: number;
  /** 0/absent when the operator did not declare it — then no vCPU count is shown. */
  vcpu?: number;
  /** What the WORKSPACE gets, as opposed to what the box has: the rung less the reserve
   *  held back for the box's own daemons (ADR 0045 決定 28). Absent on a deployment that
   *  runs uncapped — there the box IS the answer and one number is honest. */
  usable_mem_mib?: number;
}

/** The runtime's own answer, used while the profile is still loading. It matches the
 *  docker/Fargate description the UI has always shown, so nothing flickers into view. */
export const WS_SIZING_FALLBACK: WsSizing = {
  runtime: "",
  cpu_effective: true,
  mem_meaning: "limit",
  disk_meaning: "work",
};

/** How much memory a rung is worth SAYING. Two numbers exist once the workspace is
 *  capped — the machine's and the cgroup's — and the one a member can spend is the
 *  cgroup's, so that leads and the box follows in parentheses. While a deployment runs
 *  uncapped they are the same number and only one is printed. */
export function slotMemLabel(tr: (k: MsgKey, v?: Record<string, string>) => string, s: WsSlot): string {
  const usable = s.usable_mem_mib ?? 0;
  if (!usable || usable === s.mem_mib) return fmtGbHint(s.mem_mib);
  return tr("admin.ws_slot_usable", { n: fmtGbHint(usable), box: fmtGbHint(s.mem_mib) });
}

/** The slot a memory request (in MiB) lands on: the smallest rung that holds it, and
 *  the top rung when nothing does — the same rule as the CP's slotTypeFor, including
 *  0 landing on the SMALLEST slot (on Fargate/docker 0 means the deployment default
 *  instead, which is why the caller must not reuse this for those runtimes). */
export function slotFor(slots: WsSlot[] | undefined, memMib: number): WsSlot | null {
  if (!slots || slots.length === 0) return null;
  return slots.find((s) => memMib <= s.mem_mib) ?? slots[slots.length - 1];
}

/** The ladder a class id lands on: its own rungs, or the profile's default ladder when
 *  the deployment has no classes (or the id names one it no longer declares — the CP
 *  falls back the same way, so the screen must not promise a class that will not be
 *  used). */
export function ladderFor(sizing: WsSizing, classID: string): WsSlot[] | undefined {
  const cs = sizing.slot_classes;
  if (!cs || cs.length === 0) return sizing.slots;
  const want = classID || sizing.default_slot_class || cs[0].id;
  return (cs.find((c) => c.id === want) ?? cs.find((c) => c.id === sizing.default_slot_class) ?? cs[0]).slots;
}

/** The workspace sizes offered as named choices. The three axes are stored as
 *  independent numbers (ADR 0044 決定 1); these presets exist only so an admin picks
 *  from combinations Fargate actually accepts instead of discovering the matrix by
 *  trial and error. cpu is in Fargate units (1024 = 1 vCPU), mem in MiB, disk in GiB.
 *  Every pair here is a measured-valid Fargate size (docs/log/63 §63.2). */
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
