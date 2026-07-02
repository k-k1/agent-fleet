// fmtTok renders a token count compactly: 927 → "927", 30371 → "30k", 1000000 → "1M".
// Shared by the ContextBar gauge and the per-turn token readout in the chat view.
export function fmtTok(n) {
  if (!n) return "0";
  if (n >= 1e6) return (n / 1e6).toFixed(n < 1e7 ? 1 : 0).replace(/\.0$/, "") + "M";
  if (n < 1000) return String(n);
  return (n / 1000).toFixed(n < 10000 ? 1 : 0).replace(/\.0$/, "") + "k";
}
