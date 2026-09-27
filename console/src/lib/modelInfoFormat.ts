// Formatting for the model picker's catalog facts (ModelInfo, agentModels.ts; Issue #1021).
// Kept apart from the component so the number rules are unit-testable without a DOM.

/** fmtUSD writes a per-1M-token price the way price lists do: "$5", "$0.10", "$0.075", "$1.25".
 *  Three significant digits are enough to tell every model on models.dev apart, and a fixed
 *  two decimals would print gpt-6-luna's cache read ($0.01) and a $0.005 row identically. */
export function fmtUSD(n: number): string {
  if (n === 0) return "$0";
  let s = String(Number(n.toPrecision(3)));
  const dot = s.indexOf(".");
  if (dot >= 0 && s.length - dot - 1 === 1) s += "0"; // "$0.1" reads as a typo; "$0.10" does not
  return "$" + s;
}

/** fmtContext writes a token count as the vendors do: 1,050,000 → "1.05M", 204,800 → "205K". */
export function fmtContext(n: number): string {
  if (n >= 1_000_000) return String(Number((n / 1_000_000).toPrecision(3))) + "M";
  if (n >= 1_000) return String(Math.round(n / 1_000)) + "K";
  return String(n);
}
