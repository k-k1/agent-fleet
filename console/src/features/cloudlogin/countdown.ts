// Countdowns to a deadline the Agent measured, for the AWS and Google Cloud login toasts.

/** deadlineFrom turns the Agent's "seconds left" into a deadline on this browser's clock,
 *  anchored at receipt: the browser's clock may disagree with the Agent's, but it measures the
 *  interval since receipt correctly. null when the Agent gave no countdown. */
export function deadlineFrom(expiresInSec: unknown, receivedAt: number): number | null {
  return typeof expiresInSec === "number" && Number.isFinite(expiresInSec) && expiresInSec >= 0
    ? receivedAt + expiresInSec * 1000
    : null;
}

/** m:ss, or h:mm:ss from an hour up. */
export function fmtCountdown(secs: number): string {
  const s = Math.max(0, Math.floor(secs));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}
