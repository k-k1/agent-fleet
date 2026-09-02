// 各コードブロック / 引用に付くコピー・折り返しのボタン。
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
      navigator.clipboard.writeText(quote.textContent || "").then(done).catch(() => {});
    }
  });
  quote.appendChild(btn);
}
