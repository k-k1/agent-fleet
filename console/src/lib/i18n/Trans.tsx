// <Trans> — translations that carry JSX markup (docs/log/28-i18n.md §6.3). A catalog value writes
// numbered slots, either paired (`<0>…</0>`) or self-closing (`<1/>`), which are filled with the
// React elements in components[n]. t() interpolates {vars} first, so dynamic text goes through
// placeholders and only decoration and line breaks go through slots.
//
//   catalog: "session.recreate_body": "The current conversation is <0>moved to the archive</0><1/>and can be restored later."
//   usage:   <Trans k="session.recreate_body" components={[<strong />, <br />]} />
//
// Nested slots are unsupported: real usage nests shallowly, so this stays a minimal
// implementation by design. An unknown slot number passes through as its text alone.
import { Fragment, cloneElement, type ReactElement, type ReactNode } from "react";
import { useT, type MsgKey } from "./index.ts";

// Matches `<0>…</0>` (group 1 = number, group 2 = content) or `<0/>` (group 3 = number).
const SLOT_RE = /<(\d+)>([\s\S]*?)<\/\1>|<(\d+)\/>/g;

function render(tpl: string, components: ReactElement[]): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let key = 0;
  let m: RegExpExecArray | null;
  SLOT_RE.lastIndex = 0;
  while ((m = SLOT_RE.exec(tpl))) {
    if (m.index > last) out.push(<Fragment key={key++}>{tpl.slice(last, m.index)}</Fragment>);
    const idx = Number(m[1] ?? m[3]);
    const inner = m[2]; // undefined for self-closing
    const comp = components[idx];
    if (comp) {
      out.push(inner ? cloneElement(comp, { key: key++ }, inner) : cloneElement(comp, { key: key++ }));
    } else if (inner) {
      // Never lose the inner text, even when no component was supplied for the slot.
      out.push(<Fragment key={key++}>{inner}</Fragment>);
    }
    last = SLOT_RE.lastIndex;
  }
  if (last < tpl.length) out.push(<Fragment key={key++}>{tpl.slice(last)}</Fragment>);
  return out;
}

export function Trans({
  k,
  vars,
  components = [],
}: {
  k: MsgKey;
  vars?: Record<string, string | number>;
  components?: ReactElement[];
}): ReactElement {
  const tr = useT(); // re-renders on a locale change
  return <>{render(tr(k, vars), components)}</>;
}
