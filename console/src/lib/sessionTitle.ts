// Rules for a session's display name. The Agent's cleanTitle (sessionTitleMaxRunes in
// workspace/agent/session.go) is authoritative; this is a copy of it.
//
// Why the limit lives in one place: when each layer has its own, a title can be saved and edited
// and still fail at the moment of launch. Handover proposals (docs/log/77) stored up to 512 bytes,
// showed on the card and in the launch dialog and were editable, yet POST /sessions alone rejected
// anything over 80 characters, and all the user saw was "failed to launch worktree: title is too
// long".
export const SESSION_TITLE_MAX = 80;

/** Strip control characters and clamp to 80 characters (code points).
 *
 *  An input's `maxLength` only constrains typing: a long string pushed into `value` from a
 *  handover proposal or a work item passes straight through, so whoever receives a seed must run
 *  it through here. Go counts runes, so count code points with Array.from rather than the UTF-16
 *  length. */
export function clampSessionTitle(s: string): string {
  // eslint-disable-next-line no-control-regex -- same range the Agent's cleanTitle strips
  const flat = s.replace(/[\u0000-\u001f\u007f]/g, " ").trim();
  const cps = Array.from(flat);
  return cps.length > SESSION_TITLE_MAX ? cps.slice(0, SESSION_TITLE_MAX).join("").trimEnd() : flat;
}
