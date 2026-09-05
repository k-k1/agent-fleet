// Imperative pass that inserts the front matter and the repaired-table notes after the
// Markdown has rendered. The body is sanitized innerHTML, so it cannot be React nodes.
import { type TableRepair } from "../../../lib/markdown.ts";
import { t } from "../../../lib/i18n/index.ts";

// Front matter belongs above the document as a compact property list. It is
// created through DOM APIs (rather than injected HTML) so YAML scalar strings
// are always rendered as text.
// Flag each table the repair had to fix. The preview reads correctly either way, so
// without this the reader never learns the file is still broken everywhere else — on
// GitHub, in an editor, in any other Markdown viewer.
//
// Tables are matched by document order. If the renderer disagreed about how many tables
// the source holds (one nested in a blockquote, which the scanner does not look inside),
// say it once at the top rather than point at the wrong table.
export function markRepairedTables(root: HTMLElement, repair: TableRepair) {
  const notice = () => {
    const el = document.createElement("p");
    el.className = "md-table-repaired";
    el.textContent = t("view.table_repaired");
    return el;
  };
  const tables = root.querySelectorAll("table");
  if (tables.length !== repair.total) {
    root.prepend(notice());
    return;
  }
  for (const index of repair.repaired) tables[index]?.before(notice());
}

export function renderFrontMatter(root: HTMLElement, attributes: Record<string, unknown>, lenient?: boolean) {
  const panel = document.createElement("dl");
  panel.className = "md-frontmatter";
  for (const [key, value] of Object.entries(attributes)) {
    const name = document.createElement("dt");
    name.textContent = key;
    const content = document.createElement("dd");
    content.textContent = formatFrontMatterValue(value);
    panel.append(name, content);
  }
  if (!panel.childElementCount) return;
  root.prepend(panel);
  // Same bargain as a repaired table: readable here, still broken everywhere
  // else, so the reader is told rather than left to find out on GitHub.
  if (!lenient) return;
  const note = document.createElement("p");
  note.className = "md-frontmatter-note";
  note.textContent = t("view.frontmatter_invalid");
  root.prepend(note);
}

function formatFrontMatterValue(value: unknown): string {
  if (value === null) return "null";
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return String(value);
  if (value instanceof Date) return value.toISOString();
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}
