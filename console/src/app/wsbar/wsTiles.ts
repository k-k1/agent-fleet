// Pure shapes and readers for the WS bar tiles (no imports, so node tests can load it).

// Partial type covering only the fields the WS bar displays from the api/workspace/stats
// response; the Agent/CP side owns the full shape, and unknown fields
// pass through.
export interface WsStats {
  running?: boolean;
  mem_used?: number | null;
  mem_max?: number | null;
  cpu_pct?: number | null;
  oom_recent?: boolean;
  oom_killed?: boolean;
  exit_code?: number | null;
  // Home volume statfs; absent where the Agent could not read it.
  disk_used?: number | null;
  disk_total?: number | null;
  // true only when the volume is this member's own, so a threshold on it is fair.
  home_own?: boolean;
  // The workspace's own machine load. Present only on a dedicated box (ecs-ec2); the CP
  // drops it everywhere else, so absence means "not yours to see", never "idle".
  load1?: number | null;
  ncpu?: number | null;
}

export interface WsDiskTile {
  ratio: number;
  used: number;
  total: number;
  // Warn/crit levels apply only on the member's own volume.
  leveled: boolean;
}
export interface WsLoadTile {
  load1: number;
  ncpu: number;
  norm: number;
}

// Pure readers for the optional tiles: null means "render nothing", so a missing or
// nonsensical field never produces a broken tile.
const num = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);

export function wsDiskTile(d: WsStats | null): WsDiskTile | null {
  if (!d || !num(d.disk_used) || !num(d.disk_total) || d.disk_used < 0 || d.disk_total <= 0) return null;
  return {
    ratio: Math.min(1, d.disk_used / d.disk_total),
    used: d.disk_used,
    total: d.disk_total,
    leveled: !!d.home_own,
  };
}
export function wsLoadTile(d: WsStats | null): WsLoadTile | null {
  if (!d || !num(d.load1) || !num(d.ncpu) || d.load1 < 0 || d.ncpu <= 0) return null;
  return { load1: d.load1, ncpu: d.ncpu, norm: d.load1 / d.ncpu };
}
