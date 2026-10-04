// The per-session spend budget (#1054), the parts the chip, the dialog and the notification
// share. The spend is an estimate at list price (the CLI's own reported cost where it gives
// one), so every figure is written with "≈" — never as if it were the bill.

export type SpendLevel = "none" | "ok" | "warn" | "over";

/** The fraction of the cap at which the chip turns amber. */
export const SPEND_WARN_AT = 0.8;

/** "≈$1.84". Cents are the finest unit the Agent keeps for a cap, so the spend shows the same. */
export function fmtSpend(usd: number): string {
  const v = Number.isFinite(usd) && usd > 0 ? usd : 0;
  return "≈$" + v.toFixed(2);
}

/** A cap the user typed, as the Agent accepts it: dollars and cents, 0 = no budget. NaN = invalid. */
export function parseCap(text: string): number {
  const s = text.trim().replace(/^\$/, "");
  if (s === "") return 0;
  if (!/^\d+(\.\d{0,2})?$/.test(s)) return NaN;
  const v = Number(s);
  return v <= 100_000 ? v : NaN;
}

export function spendLevel(spend: number, cap: number | undefined): SpendLevel {
  if (!cap || cap <= 0) return "none";
  if (spend >= cap) return "over";
  if (spend >= cap * SPEND_WARN_AT) return "warn";
  return "ok";
}

/** The value "raise and resume" starts from: half again the old cap, and in any case a quarter
 *  above what is already spent — a raise that left the spend at the cap would stop the session
 *  again after one turn. Whole dollars, so the dialog does not open on $7.83. */
export function suggestedRaise(cap: number, spend: number): number {
  return Math.max(1, Math.ceil(Math.max(cap * 1.5, spend * 1.25, cap + 1)));
}

/** The default-budget choices in Settings (USD; 0 = none). */
export const SPEND_CAP_DEFAULTS = [0, 1, 2, 5, 10, 20, 50, 100];
