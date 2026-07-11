// Clipboard helper for the rail's copy menu items. Returns whether the write
// succeeded (the Clipboard API is absent on non-secure origins) — the caller
// toasts the outcome.
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {}
  return false;
}
