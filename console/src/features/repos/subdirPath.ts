// Pure path helper for the working-directory field, kept out of SubdirPicker.tsx so the
// node test project can import it without dragging in the api client (which touches
// localStorage at module load).
/** normalizeSubdir trims the decoration users paste in ("./x", "/x", "x/") down to the
 * slash-relative form the wire wants. Rejection of ".." escapes is the Agent's job
 * (session.CleanSubdir) — this only keeps the common cases tidy. */
export function normalizeSubdir(s: string): string {
  return s.trim().replace(/\\/g, "/").replace(/^\.\//, "").replace(/^\/+|\/+$/g, "");
}
