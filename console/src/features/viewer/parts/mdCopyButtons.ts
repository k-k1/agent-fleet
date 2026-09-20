// The copy and wrap buttons attached to each code block / blockquote.
import { t } from "../../../lib/i18n/index.ts";

// addCopyButton pins code actions at the bottom-right of a fenced code block: copy
// copies exactly that block, while wrap toggles its own line wrapping. Imperative
// because the markdown is rendered as sanitized innerHTML, not React nodes.
export function addCopyButton(code: HTMLElement, wrapDefault: boolean) {
  const pre = code.parentElement;
  if (!pre) return;
  // Wrap the <pre> so the button pins to the visible bottom-right corner rather than
  // scrolling away with the code (the <pre> itself is overflow:auto).
  if (pre.parentElement?.classList.contains("md-pre-wrap")) return; // already wrapped
  const wrap = document.createElement("div");
  wrap.className = "md-pre-wrap";
  pre.replaceWith(wrap);
  wrap.appendChild(pre);
  const actions = document.createElement("div");
  actions.className = "md-code-actions";

  const wrapBtn = document.createElement("button");
  wrapBtn.type = "button";
  wrapBtn.className = "md-code-action md-code-wrap-toggle";
  const updateWrapLabel = (enabled: boolean) => {
    const label = t(enabled ? "ui.unwrap_lines" : "ui.wrap_lines");
    wrapBtn.title = label;
    wrapBtn.setAttribute("aria-label", label);
    wrapBtn.setAttribute("aria-pressed", String(enabled));
  };
  wrapBtn.innerHTML = '<i class="codicon codicon-word-wrap"></i>';
  if (wrapDefault) pre.classList.add("md-code-wrap");
  updateWrapLabel(wrapDefault);
  wrapBtn.addEventListener("click", () => {
    updateWrapLabel(pre.classList.toggle("md-code-wrap"));
  });

  const copyBtn = document.createElement("button");
  copyBtn.type = "button";
  copyBtn.className = "md-code-action md-copy";
  copyBtn.title = t("view.copy_this_code");
  copyBtn.setAttribute("aria-label", t("view.copy_this_code"));
  copyBtn.innerHTML = '<i class="codicon codicon-copy"></i>';
  copyBtn.addEventListener("click", () => {
    const text = code.textContent || "";
    const done = () => {
      copyBtn.classList.add("copied");
      copyBtn.innerHTML = '<i class="codicon codicon-check"></i>';
      setTimeout(() => {
        copyBtn.classList.remove("copied");
        copyBtn.innerHTML = '<i class="codicon codicon-copy"></i>';
      }, 1200);
    };
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(done).catch(() => {});
    }
  });
  actions.append(wrapBtn, copyBtn);
  wrap.appendChild(actions);
}

const QUOTE_PARAGRAPH_TAGS = new Set([
  "ADDRESS", "BLOCKQUOTE", "DIV", "H1", "H2", "H3", "H4", "H5", "H6", "P", "PRE",
]);

// textContent discards the boundary between rendered paragraphs while retaining
// incidental whitespace from the HTML. Recreate only the semantic boundaries so
// copied prose does not depend on how the renderer indented or wrapped its markup.
export function quoteCopyText(quote: HTMLElement): string {
  const copy = quote.cloneNode(true) as HTMLElement;
  copy.querySelectorAll("button").forEach((button) => button.remove());
  const doc = copy.ownerDocument;

  copy.querySelectorAll("br").forEach((br) => br.replaceWith(doc.createTextNode("\n")));
  copy.querySelectorAll("th, td").forEach((cell) => cell.append(doc.createTextNode("\t")));
  copy.querySelectorAll("li, tr").forEach((row) => row.append(doc.createTextNode("\n")));
  copy.querySelectorAll<HTMLElement>("address, blockquote, div, h1, h2, h3, h4, h5, h6, p, pre")
    .forEach((block) => {
      if (QUOTE_PARAGRAPH_TAGS.has(block.tagName)) block.append(doc.createTextNode("\n\n"));
    });

  return (copy.textContent || "")
    .replace(/\r\n?/g, "\n")
    .split("\n")
    .map((line) => line.trimEnd())
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .replace(/^(?:[ \t]*\n)+|(?:\n[ \t]*)+$/g, "");
}

// addQuoteCopyButton adds a copy action directly to a rendered quote. Unlike code
// blocks, quotes do not scroll, so the action can be positioned inside the quote's
// top-right corner without an extra wrapper.
export function addQuoteCopyButton(quote: HTMLElement) {
  if (quote.classList.contains("md-quote-copy")) return;
  quote.classList.add("md-quote-copy");
  const btn = document.createElement("button");
  const label = t("view.copy_this_quote");
  btn.type = "button";
  btn.className = "md-code-action md-copy md-quote-copy-button";
  btn.title = label;
  btn.setAttribute("aria-label", label);
  btn.innerHTML = '<i class="codicon codicon-copy"></i>';
  btn.addEventListener("click", () => {
    const done = () => {
      btn.classList.add("copied");
      btn.innerHTML = '<i class="codicon codicon-check"></i>';
      setTimeout(() => {
        btn.classList.remove("copied");
        btn.innerHTML = '<i class="codicon codicon-copy"></i>';
      }, 1200);
    };
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(quoteCopyText(quote)).then(done).catch(() => {});
    }
  });
  quote.appendChild(btn);
}
