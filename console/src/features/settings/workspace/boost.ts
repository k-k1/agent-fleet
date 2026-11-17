// The sizing answer (GET /api/workspace/sizing) and the rules for reading it.
//
// The CP resolves which box the next start runs on (control-plane/workspace_boost.go:
// boost > auto > stored > tenant default) and says so here. The Console draws THAT answer and
// never recomputes the box from the stored size: slotFor / ladderFor still draw the ladder, but
// they no longer decide what a member "gets".

export interface SizingBox {
  class?: string;
  class_label?: string;
  instance_type?: string;
  arch?: string;
  vcpu?: number;
  mem_mib: number;
  usable_mem_mib?: number;
}

export interface SizingBoost {
  enabled: boolean;
  active: boolean;
  /** Pressing Boost would do something. Otherwise `reason` says why not. */
  available: boolean;
  reason?: "unsupported" | "disabled" | "top_rung" | "limit" | "budget_spent";
  /** The box a press gives, or the box the live boost gave. */
  target?: SizingBox;
  default_hours: number;
  max_hours: number;
  budget: { monthly_hours: number; counted: boolean; used_hours: number; spent: boolean };
}

export interface WsSizingView {
  supported: boolean;
  runtime: string;
  stored?: SizingBox;
  next_start?: { box: SizingBox; source: "stored" | "boost" | "auto"; until?: string; note?: string };
  /** Absent while stopped, or when the Agent could not say which box it is on. */
  running?: SizingBox;
  /** The workspace runs on a SMALLER box than the next start's: a restart would land on a
   *  bigger one, and ends the sessions running in it. Never done on its own. */
  restart_offered: boolean;
  boost: SizingBoost;
}

/** "m7i.xlarge · 16 GiB", dropping what is unknown. */
export function boxText(b: SizingBox | undefined): string {
  if (!b) return "";
  const gib = b.mem_mib / 1024;
  const mem = (Number.isInteger(gib) ? String(gib) : gib.toFixed(1)) + " GiB";
  return [b.instance_type, mem].filter(Boolean).join(" · ");
}

/** The i18n key for why Boost is unavailable; null when it is available. */
export function boostReasonKey(b: SizingBoost): string | null {
  if (b.available) return null;
  switch (b.reason) {
    case "disabled":
      return "boost.reason_disabled";
    case "top_rung":
      return "boost.reason_top_rung";
    case "limit":
      return "boost.reason_limit";
    case "budget_spent":
      return "boost.reason_budget_spent";
    default:
      return "boost.reason_unsupported";
  }
}
