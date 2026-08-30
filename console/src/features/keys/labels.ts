// Command/group title resolution for the keyboard system. A Command.title (and
// Group.title) is now an i18n message key, optionally carrying a "|k=v" vars suffix
// (the generated pane-focus commands use it to pass {n}). Display resolves to the
// current locale; the palette searches across ALL locales so typing English or Japanese
// matches either way (docs/log/29, i18n = docs/log/28). Kept out of the pure lib layer.
import { tMaybe, tLocales } from "../../lib/i18n/index.ts";

function parse(title: string): { key: string; vars?: Record<string, string> } {
  const bar = title.indexOf("|");
  if (bar === -1) return { key: title };
  const vars: Record<string, string> = {};
  for (const pair of title.slice(bar + 1).split(",")) {
    const eq = pair.indexOf("=");
    if (eq > 0) vars[pair.slice(0, eq)] = pair.slice(eq + 1);
  }
  return { key: title.slice(0, bar), vars };
}

/** Current-locale display label for a command/group title. Falls back to the raw string
 * — e.g. a which-key nested-group key char ("1") that is not a message key. */
export function cmdLabel(title: string): string {
  const { key, vars } = parse(title);
  return tMaybe(key, vars) ?? title;
}

/** All-locale search text (ja + en + …) for a command title, so the command palette's
 * fuzzy filter matches regardless of the language the user types. Falls back to raw. */
export function cmdSearch(title: string): string {
  const { key, vars } = parse(title);
  const all = tLocales(key, vars);
  return all.length ? all.join(" ") : title;
}
