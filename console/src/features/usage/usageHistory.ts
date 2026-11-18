// Wire type and formatting for the per-member usage history (ADR 0139 decision 3).
//
// Every figure the Agent may not have measured arrives as an explicit `null`. It must never be
// shown as 0: "0 minutes above the threshold" claims a healthy box, "not measured" claims nothing.

// cpu_box_*, vcpu, burst_baseline_pct and above_baseline_minutes are carried but not drawn:
// they need the baseline comparison, which is a later view.
export interface UsageHistoryDay {
  day: string;
  tenant?: string;
  user_key?: string;
  email?: string;
  instance_type: string;
  class?: string;
  override?: string;
  vcpu: number | null;
  burst_baseline_pct: number | null;
  running_minutes: number | null;
  measured_minutes: { cpu: number; working_set: number; psi: number; refault: number };
  cpu_p50: number | null;
  cpu_p95: number | null;
  cpu_box_p50: number | null;
  cpu_box_p95: number | null;
  working_set_p95: number | null;
  working_set_max: number | null;
  psi_full_avg60_max: number | null;
  psi_warn_minutes: number | null;
  refault_p95: number | null;
  above_baseline_minutes: number | null;
}

/** Formats a figure, or returns null when it is unmeasured (the caller draws the label). */
export function figure(v: number | null | undefined, fmt: (n: number) => string): string | null {
  return typeof v === "number" && Number.isFinite(v) ? fmt(v) : null;
}

export function fmtPct(n: number): string {
  return n.toFixed(n < 10 ? 1 : 0) + "%";
}

export function fmtMinutes(n: number): string {
  return (n < 10 ? n.toFixed(1) : String(Math.round(n))) + " min";
}

export function fmtRate(n: number): string {
  return n.toFixed(n < 10 ? 2 : 1) + "/s";
}

export function fmtBytes(n: number): string {
  const MiB = 1024 * 1024;
  if (n >= 1024 * MiB) return (n / (1024 * MiB)).toFixed(1) + " GiB";
  return Math.round(n / MiB) + " MiB";
}
