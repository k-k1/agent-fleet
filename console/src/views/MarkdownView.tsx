import { useEffect, useRef } from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";
import hljs from "highlight.js/lib/common";
import { dirName, baseName, joinPath, isExternalUrl, slug } from "../lib/filemeta.js";
import { api, downloadURL } from "../api.js";

// MarkdownView renders Markdown to sanitized HTML, highlights fenced code blocks,
// turns ```mermaid blocks into rendered diagrams (lazy-loaded), and wires links:
// external URLs open in a new tab, in-page #anchors scroll, and repo-relative
// links open that file in the viewer via onOpenFile.
let mermaidSeq = 0;

interface MarkdownViewProps {
  source?: string;
  basePath?: string;
  breaks?: boolean; // treat a single newline as <br> (chat prompts keep their line breaks)
  onOpenFile?: (path: string) => void;
  onOpenDir?: (path: string) => void; // a relative link to a directory → reveal in FILES
}

export default function MarkdownView({ source, basePath = "", breaks = false, onOpenFile, onOpenDir }: MarkdownViewProps) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    let alive = true;

    const rawHtml = marked.parse(source ?? "", { gfm: true, breaks }) as string;
    el.innerHTML = DOMPurify.sanitize(rawHtml);

    renderEmoji(el); // :shortcode: → emoji (skips code / pre)

    // Give headings slug ids so in-page #anchors can resolve.
    el.querySelectorAll("h1,h2,h3,h4,h5,h6").forEach((h) => {
      if (!h.id) h.id = slug(h.textContent || "");
    });

    wireLinks(el, basePath, onOpenFile, onOpenDir);
    wireImages(el, basePath);

    // VS Code-style sticky headings: when this markdown is inside a scroll container
    // (.md-scroll — the Doc / File viewers, not chat bubbles), pin the heading path of
    // the current scroll position at the top. Purely imperative since the body is
    // rendered as innerHTML. Cleaned up when the effect re-runs / unmounts.
    let stickyCleanup = () => {};
    const scroller = el.closest<HTMLElement>(".md-scroll");
    if (scroller) stickyCleanup = setupStickyHeadings(el, scroller);

    // Syntax-highlight every fenced code block except mermaid (handled below), and
    // give each one a copy button (bottom-right) that copies just that block.
    el.querySelectorAll<HTMLElement>("pre > code").forEach((code) => {
      if (code.classList.contains("language-mermaid")) return;
      try {
        hljs.highlightElement(code);
      } catch {}
      addCopyButton(code);
    });

    // Render mermaid diagrams, replacing their <pre> with the produced SVG.
    const blocks = [...el.querySelectorAll<HTMLElement>("pre > code.language-mermaid")];
    if (blocks.length) {
      import("mermaid").then(({ default: mermaid }) => {
        if (!alive) return;
        mermaid.initialize({ startOnLoad: false, theme: "dark", securityLevel: "strict" });
        blocks.forEach(async (code) => {
          const src = code.textContent || "";
          const id = "mmd-" + ++mermaidSeq;
          try {
            const { svg } = await mermaid.render(id, src);
            if (!alive || !code.parentElement) return;
            const wrap = document.createElement("div");
            wrap.className = "mermaid-diagram";
            wrap.innerHTML = svg;
            code.parentElement.replaceWith(wrap);
          } catch {
            // leave the source block in place on a render error
          }
        });
      });
    }

    return () => {
      alive = false;
      stickyCleanup();
    };
  }, [source, basePath, breaks, onOpenFile]);

  return <div className="markdown" ref={ref} />;
}

// Common GitHub-style emoji shortcodes (:tada: → 🎉). A curated subset covering what
// shows up in dev docs — unknown codes are left as literal text (no regression). Mirrors
// CodeLeaf's EmojiParser intent without pulling in a full ~1800-entry emoji table.
const EMOJI: Record<string, string> = {
  smile: "😄", smiley: "😃", grin: "😁", laughing: "😆", wink: "😉", blush: "😊",
  joy: "😂", sweat_smile: "😅", thinking: "🤔", eyes: "👀", tada: "🎉", rocket: "🚀",
  fire: "🔥", sparkles: "✨", star: "⭐", star2: "🌟", zap: "⚡", boom: "💥",
  bulb: "💡", warning: "⚠️", white_check_mark: "✅", heavy_check_mark: "✔️",
  x: "❌", negative_squared_cross_mark: "❎", question: "❓", exclamation: "❗",
  bangbang: "‼️", "100": "💯", bug: "🐛", memo: "📝", pencil: "✏️", pencil2: "✏️",
  books: "📚", book: "📖", clipboard: "📋", package: "📦", gear: "⚙️", wrench: "🔧",
  hammer: "🔨", lock: "🔒", unlock: "🔓", key: "🔑", mag: "🔍", link: "🔗",
  pushpin: "📌", label: "🏷️", dart: "🎯", trophy: "🏆", rotating_light: "🚨",
  construction: "🚧", no_entry: "⛔", no_entry_sign: "🚫", recycle: "♻️",
  checkered_flag: "🏁", bell: "🔔", email: "📧", "e-mail": "📧",
  speech_balloon: "💬", robot: "🤖", computer: "💻", floppy_disk: "💾",
  hourglass: "⏳", calendar: "📅", clock: "🕐", heart: "❤️", broken_heart: "💔",
  "+1": "👍", thumbsup: "👍", "-1": "👎", thumbsdown: "👎", ok_hand: "👌",
  raised_hands: "🙌", clap: "👏", pray: "🙏", point_right: "👉", point_left: "👈",
  wave: "👋", muscle: "💪", ghost: "👻", thread: "🧵",
  arrow_right: "➡️", arrow_left: "⬅️", arrow_up: "⬆️", arrow_down: "⬇️",
};
const EMOJI_RE = /:([a-z0-9_+-]+):/gi;

// renderEmoji replaces :shortcode: in text nodes (skipping code / pre) with the emoji.
function renderEmoji(root: HTMLElement) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(n) {
      if (!n.nodeValue || n.nodeValue.indexOf(":") < 0) return NodeFilter.FILTER_REJECT;
      return (n.parentElement?.closest("code,pre"))
        ? NodeFilter.FILTER_REJECT
        : NodeFilter.FILTER_ACCEPT;
    },
  });
  const targets: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) targets.push(n as Text);
  for (const t of targets) {
    const next = t.nodeValue!.replace(EMOJI_RE, (whole, code: string) => EMOJI[code.toLowerCase()] ?? whole);
    if (next !== t.nodeValue) t.nodeValue = next;
  }
}

// setupStickyHeadings pins the heading breadcrumb (# > ## > ###) of the current scroll
// position to the top of the scroller, VS Code-style. An absolutely-positioned overlay
// appended to the scroller is repositioned to the viewport top on scroll and its rows
// rebuilt to the active heading chain. Returns a cleanup that removes it. Heading tops
// are measured live (getBoundingClientRect) so async layout shifts — mermaid diagrams,
// images — self-correct on the next scroll frame.
const MAX_STICKY_HEADS = 4; // keep the nearest N so a deep doc doesn't bury the content

function setupStickyHeadings(md: HTMLElement, scroller: HTMLElement): () => void {
  const heads = [...md.querySelectorAll<HTMLElement>("h1,h2,h3,h4,h5,h6")].map((h) => ({
    el: h,
    level: Number(h.tagName[1]),
    text: h.textContent || "",
  }));
  if (!heads.length) return () => {};

  const bar = document.createElement("div");
  bar.className = "md-sticky";
  bar.setAttribute("aria-hidden", "true");
  scroller.appendChild(bar);

  const build = () => {
    const st = scroller.scrollTop;
    const base = scroller.getBoundingClientRect().top - st; // content-space origin
    // The active chain: for every heading at/above the viewport top, keep a stack where
    // a heading pops any siblings/deeper of >= its level, so the stack is its ancestry.
    const stack: { el: HTMLElement; level: number; text: string }[] = [];
    for (const h of heads) {
      const top = h.el.getBoundingClientRect().top - base;
      if (top > st + 4) break; // headings are in document order → the rest are below
      while (stack.length && stack[stack.length - 1].level >= h.level) stack.pop();
      stack.push(h);
    }
    const shown = stack.length > MAX_STICKY_HEADS ? stack.slice(stack.length - MAX_STICKY_HEADS) : stack;
    if (!shown.length) {
      bar.style.display = "none";
      return;
    }
    bar.style.display = "";
    bar.style.top = st + "px";
    bar.textContent = "";
    for (const h of shown) {
      const row = document.createElement("div");
      row.className = "md-sticky-row md-sticky-h" + h.level;
      row.textContent = h.text;
      row.title = h.text;
      row.addEventListener("click", () => h.el.scrollIntoView({ block: "start" }));
      bar.appendChild(row);
    }
  };

  let raf = 0;
  const onScroll = () => {
    if (raf) return;
    raf = requestAnimationFrame(() => {
      raf = 0;
      build();
    });
  };
  scroller.addEventListener("scroll", onScroll, { passive: true });
  window.addEventListener("resize", onScroll);
  build();

  return () => {
    if (raf) cancelAnimationFrame(raf);
    scroller.removeEventListener("scroll", onScroll);
    window.removeEventListener("resize", onScroll);
    bar.remove();
  };
}

// addCopyButton pins a copy control at the bottom-right of a fenced code block that
// copies exactly that block's text (not the whole message). Imperative because the
// markdown is rendered as sanitized innerHTML, not React nodes.
function addCopyButton(code: HTMLElement) {
  const pre = code.parentElement;
  if (!pre) return;
  // Wrap the <pre> so the button pins to the visible bottom-right corner rather than
  // scrolling away with the code (the <pre> itself is overflow:auto).
  if (pre.parentElement?.classList.contains("md-pre-wrap")) return; // already wrapped
  const wrap = document.createElement("div");
  wrap.className = "md-pre-wrap";
  pre.replaceWith(wrap);
  wrap.appendChild(pre);
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "md-copy";
  btn.title = "このコードをコピー";
  btn.innerHTML = '<i class="codicon codicon-copy"></i>';
  btn.addEventListener("click", () => {
    const text = code.textContent || "";
    const done = () => {
      btn.classList.add("copied");
      btn.innerHTML = '<i class="codicon codicon-check"></i>';
      setTimeout(() => {
        btn.classList.remove("copied");
        btn.innerHTML = '<i class="codicon codicon-copy"></i>';
      }, 1200);
    };
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(done).catch(() => {});
    }
  });
  wrap.appendChild(btn);
}

// resolveRelPath turns a repo-relative Markdown href/src into a home-relative fs path.
// marked percent-encodes non-ASCII (日本語 → %E6…), so decode first or the path won't
// resolve; a literal-% name that isn't valid encoding falls back to the raw string.
// A leading "/" resolves from the repo root, everything else from the file's own dir.
function resolveRelPath(ref: string, basePath: string): string {
  const m = basePath.match(/^(repos\/[^/]+)\//);
  const repoRoot = m ? m[1] : dirName(basePath);
  const fileDir = dirName(basePath);
  let p = ref.split("#")[0].split("?")[0];
  if (!p) return "";
  try {
    p = decodeURIComponent(p);
  } catch {}
  const base = p.startsWith("/") ? repoRoot : fileDir;
  return joinPath(base, p.replace(/^\/+/, ""));
}

// wireImages rewrites relative <img src> to the file-download endpoint so repo-local
// images (including Japanese filenames) actually load; browser-relative srcs would
// otherwise 404 against the console origin. Scheme / protocol-relative / data: URLs
// are left as-is.
function wireImages(el: HTMLElement, basePath: string) {
  el.querySelectorAll<HTMLImageElement>("img[src]").forEach((img) => {
    const src = img.getAttribute("src") || "";
    if (!src || src.startsWith("#") || isExternalUrl(src)) return;
    const target = resolveRelPath(src, basePath);
    if (target) img.setAttribute("src", downloadURL(target));
  });
}

// openRepoTarget resolves a repo-internal path to a file (open in viewer) or directory
// (reveal in FILES), or silently ignores it when it doesn't exist — mirroring CodeLeaf's
// RepoLinkResolver. One listing of the parent tells file vs dir vs missing in a single
// request (missing entries include denylisted paths, which we also ignore).
async function openRepoTarget(
  target: string,
  onOpenFile?: (p: string) => void,
  onOpenDir?: (p: string) => void,
) {
  let d: { entries?: { name: string; type: string }[] } | null = null;
  try {
    d = await api(`api/fs/tree?path=${encodeURIComponent(dirName(target))}`);
  } catch {
    return;
  }
  const entry = (d?.entries || []).find((e) => e.name === baseName(target));
  if (!entry) return; // nonexistent / denylisted → ignore (no error toast)
  if (entry.type === "dir") onOpenDir?.(target);
  else onOpenFile?.(target);
}

// wireLinks classifies and rewires every <a> in the rendered markdown.
function wireLinks(
  el: HTMLElement,
  basePath: string,
  onOpenFile?: (path: string) => void,
  onOpenDir?: (path: string) => void,
) {
  el.querySelectorAll<HTMLAnchorElement>("a[href]").forEach((a) => {
    const href = a.getAttribute("href") || "";

    if (href.startsWith("#")) {
      a.classList.add("anchor-link");
      a.addEventListener("click", (e) => {
        e.preventDefault();
        const id = decodeURIComponent(href.slice(1));
        let t: Element | null = null;
        try {
          t = el.querySelector("#" + CSS.escape(id));
        } catch {}
        t?.scrollIntoView({ behavior: "smooth", block: "start" });
      });
      return;
    }

    if (isExternalUrl(href)) {
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      a.classList.add("ext-link");
      return;
    }

    // Repo-internal relative link → open a file in the viewer or reveal a directory in
    // FILES (path decoded + resolved by the shared helper, so Japanese names work).
    a.classList.add("repo-link");
    a.addEventListener("click", (e) => {
      e.preventDefault();
      const target = resolveRelPath(href, basePath);
      if (target) openRepoTarget(target, onOpenFile, onOpenDir);
    });
  });
}
