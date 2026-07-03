// Bytes → GiB with adaptive precision: 2 decimals under 10 (so 0.98 stays visible),
// 1 above (so 26.9 stays compact). Returns the bare number (no unit) — callers add
// their own "G"/"GiB" suffix. Shared by the WsBar memory chip and AdminTab.
export const GiB = 1073741824;

export const fmtGiB = (b: number): string => {
  const v = b / GiB;
  return v < 10 ? v.toFixed(2) : v.toFixed(1);
};
