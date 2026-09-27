/**
 * Split a name for middle truncation: the head may shrink to "…", the tail never
 * does. Long file names in one folder usually share a prefix and differ at the end
 * (a version, a suffix, the extension), so a right-side ellipsis hides exactly the
 * part that tells them apart.
 *
 * The tail is the last path segment when it is short enough to keep whole (a
 * changes-list path keeps its file name); otherwise the extension plus the last
 * few characters of the stem. Returns an empty head when there is nothing worth
 * truncating separately.
 */
export function splitMiddle(text: string): [head: string, tail: string] {
  const base = text.slice(text.lastIndexOf("/") + 1);
  const ext = /\.[^.]{1,8}$/.exec(base)?.[0] ?? "";
  let n = ext.length + STEM_TAIL;
  if (base.length < text.length && base.length <= WHOLE_BASE) n = base.length;
  // A head of a few characters would collapse to "…" and save nothing.
  if (text.length - n < MIN_HEAD) return ["", text];
  return [text.slice(0, -n), text.slice(-n)];
}

const STEM_TAIL = 10;
const WHOLE_BASE = 24;
const MIN_HEAD = 4;
