import { useEffect, useRef } from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";
import hljs from "highlight.js/lib/common";
import { dirName, baseName, isExternalUrl, resolveMarkdownFileTarget, slug } from "../../lib/filemeta.ts";
import { api, downloadURL } from "../../core/api/client.ts";
import { useSettings } from "../../lib/settings.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t } from "../../lib/i18n/index.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat } from "../sessions/open.ts";
import { openCommit } from "../scm/open.ts";

// MarkdownView renders Markdown to sanitized HTML, highlights fenced code blocks,
// turns ```mermaid blocks into rendered diagrams (lazy-loaded), and wires links:
// external URLs open in a new tab, in-page #anchors scroll, and repo-relative
// links open that file in the viewer via onOpenFile.
let mermaidSeq = 0;

interface MarkdownViewProps {
  source?: string;
  basePath?: string;
  baseDir?: string; // cwd for an agent reply; relative file citations resolve from here
  // SCM repo id (Session.repo) this markdown is "about" — supplied by the mirror from the
  // session it renders. Only when present do bare commit hashes get linkified (a hash is
  // meaningless without a repo to resolve it in); session slugs linkify regardless.
  repo?: string | null;
  breaks?: boolean; // treat a single newline as <br> (chat prompts keep their line breaks)
  // Lightweight mode for a live-accumulating source (a streaming chat reply): the effect
  // re-runs on every delta, so only parse + sanitize + highlight run — mermaid (would
  // render half-written diagrams over and over), link/image wiring and per-block copy
  // buttons are skipped; the finished message re-renders through the full path anyway.
  // A blinking caret marks the tail of the last block.
  streaming?: boolean;
  /** openInNew is true for Ctrl/Cmd-click or a middle click. */
  onOpenFile?: (path: string, line?: number, column?: number, openInNew?: boolean) => void;
  onOpenDir?: (path: string) => void; // a relative link to a directory → reveal in FILES
}

export function MarkdownView({
  source,
  basePath = "",
  baseDir = "",
  repo = null,
  breaks = false,
  streaming = false,
  onOpenFile,
  onOpenDir,
}: MarkdownViewProps) {
  const ref = useRef<HTMLDivElement>(null);
  const toast = useToast();
  // Follow the app theme so mermaid diagrams re-render in matching colors.
  const theme = useSettings().theme === "light" ? "light" : "dark";
  // Callers pass inline arrow callbacks (new identity every render), and panes
  // re-render on hover/focus — keep the latest callbacks in refs so the render
  // effect doesn't depend on them. Otherwise every pane hover re-parsed the whole
  // document and re-rendered mermaid async, making the preview width flap.
  const onOpenFileRef = useRef(onOpenFile);
  const onOpenDirRef = useRef(onOpenDir);
  onOpenFileRef.current = onOpenFile;
  onOpenDirRef.current = onOpenDir;

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    let alive = true;

    const rawHtml = marked.parse(source ?? "", { gfm: true, breaks }) as string;
    el.innerHTML = DOMPurify.sanitize(rawHtml);

    renderEmoji(el); // :shortcode: → emoji (skips code / pre)

    if (streaming) {
      el.querySelectorAll<HTMLElement>("pre > code").forEach((code) => {
        if (code.classList.contains("language-mermaid")) return; // leave as plain source
        try {
          hljs.highlightElement(code);
        } catch {}
      });
      const caret = document.createElement("span");
      caret.className = "md-stream-caret";
      caret.textContent = "▍";
      (el.lastElementChild ?? el).appendChild(caret);
      return;
    }

    // Give headings slug ids so in-page #anchors can resolve.
    el.querySelectorAll("h1,h2,h3,h4,h5,h6").forEach((h) => {
      if (!h.id) h.id = slug(h.textContent || "");
    });

    wireLinks(
      el,
      basePath,
      baseDir,
      (p, line, column, openInNew) => onOpenFileRef.current?.(p, line, column, openInNew),
      (p) => onOpenDirRef.current?.(p),
      (message) => toast(message),
    );
    wireImages(el, basePath);

    // Auto-link bare git commit hashes (→ commit pane) and session slugs (→ that
    // session's mirror) mentioned in prose. Runs AFTER wireLinks so the anchors it
    // creates (no href) aren't reclassified as file links; skips code / pre / existing
    // anchors like renderEmoji does. A slug only links when the session actually exists;
    // a hash links optimistically and is verified against the repo on click.
    linkifyRefs(el, repo, (message) => toast(message));

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
        mermaid.initialize({
          startOnLoad: false,
          theme: theme === "light" ? "default" : "dark",
          securityLevel: "strict",
        });
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
  }, [source, basePath, baseDir, repo, breaks, streaming, theme, toast]);

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

// linkifyRefs turns bare git commit hashes and session slugs into clickable links,
// mirroring renderEmoji's text-node walk (skips existing anchors so we never double-link).
// Matches are scanned in one combined pass so the two token shapes can't overlap or fight
// over the same run of text.
//
// - commit hash: 7–40 hex chars. Only linkified when a `repo` is known (a hash is
//   unresolvable without one). Linked optimistically; the click handler verifies the sha
//   exists in the repo before opening the commit pane, and toasts otherwise — so a hex
//   token that isn't actually a commit does nothing but tell you so ("存在しない→リンクにしない").
// - session slug: `s` + digits (Session.name). Only linkified when a session with that
//   exact name currently exists (looked up live in the sessions store); a stale/unknown
//   slug is left as plain text.
//
// Code context: a fenced block (<pre>) is literal source and is never touched. INLINE
// code (`s7` written in backticks — the common way a slug is mentioned) IS linkified, but
// only for slugs: a hash in inline code stays literal, since a commit id in backticks is
// usually a copy-me literal, whereas a bare `sN` in backticks is a reference to that session.
const COMMIT_RE = "[0-9a-f]{7,40}";
const SLUG_RE = "s\\d+";
// Combined, alternation ordered longest-first; \b keeps us off sub-word matches.
const REF_RE = new RegExp(`\\b(?:${COMMIT_RE}|${SLUG_RE})\\b`, "g");

function linkifyRefs(root: HTMLElement, repo: string | null, onError: (message: string) => void) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(n) {
      if (!n.nodeValue || !/[0-9a-z]/i.test(n.nodeValue)) return NodeFilter.FILTER_REJECT;
      // Never touch fenced blocks (literal source) or text already inside a link. Inline
      // code IS walked — a slug in backticks still linkifies (gated below to slug-only).
      return n.parentElement?.closest("pre,a") ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT;
    },
  });
  const targets: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) targets.push(n as Text);

  for (const node of targets) {
    const text = node.nodeValue!;
    // Inside inline <code>, only slugs linkify — a hash in backticks stays literal.
    const inCode = !!node.parentElement?.closest("code");
    REF_RE.lastIndex = 0;
    let m = REF_RE.exec(text);
    if (!m) continue;

    const out = document.createDocumentFragment();
    let last = 0;
    do {
      const token = m[0];
      const isCommit = /^[0-9a-f]{7,40}$/.test(token) && !/^s\d+$/.test(token);
      let a: HTMLAnchorElement | null = null;
      if (isCommit) {
        if (repo && !inCode) a = makeCommitLink(token, repo, onError);
      } else {
        // slug shape (s\d+): link only if that session exists right now
        const exists = useSessionsStore.getState().sessions.some((s) => s.name === token);
        if (exists) a = makeSessionLink(token);
      }
      if (a) {
        if (m.index > last) out.appendChild(document.createTextNode(text.slice(last, m.index)));
        out.appendChild(a);
        last = m.index + token.length;
      }
      m = REF_RE.exec(text);
    } while (m);

    if (last === 0) continue; // nothing linkified in this node — leave it untouched
    if (last < text.length) out.appendChild(document.createTextNode(text.slice(last)));
    node.parentNode?.replaceChild(out, node);
  }
}

// makeCommitLink builds a non-navigating anchor for a bare sha. On click it verifies the
// commit resolves in `repo` (GET show → 404 when absent) before opening the commit pane;
// a missing / unresolvable sha just toasts, so the link never opens an empty pane.
function makeCommitLink(sha: string, repo: string, onError: (message: string) => void): HTMLAnchorElement {
  const a = document.createElement("a");
  a.className = "md-ref-link md-commit-link";
  a.textContent = sha.length > 12 ? sha.slice(0, 10) : sha; // display a short sha
  a.setAttribute("role", "link");
  a.tabIndex = 0;
  a.title = t("view.open_commit", { sha: sha.slice(0, 10) });
  const open = async () => {
    let d: { hash?: string; error?: unknown } | null = null;
    try {
      d = await api(`api/repos/${encodeURIComponent(repo)}/show?sha=${encodeURIComponent(sha)}`);
    } catch {
      d = null;
    }
    if (!d || !d.hash) {
      onError(t("view.commit_not_found", { sha: sha.slice(0, 10) }));
      return;
    }
    openCommit(repo, sha);
  };
  a.addEventListener("click", (e) => {
    e.preventDefault();
    void open();
  });
  a.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      void open();
    }
  });
  return a;
}

// makeSessionLink builds a non-navigating anchor that opens a session's chat mirror.
function makeSessionLink(name: string): HTMLAnchorElement {
  const a = document.createElement("a");
  a.className = "md-ref-link md-session-link";
  a.textContent = name;
  a.setAttribute("role", "link");
  a.tabIndex = 0;
  a.title = t("view.open_session", { name });
  const open = () => openSessionChat(name);
  a.addEventListener("click", (e) => {
    e.preventDefault();
    open();
  });
  a.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      open();
    }
  });
  return a;
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

  let lastSig: string | null = null; // signature of the currently-rendered chain (never a real value)

  // pin keeps the overlay glued to the viewport top; cheap, so it runs on every scroll
  // event (no rAF lag → no jitter). It never touches the DOM rows.
  const pin = () => {
    bar.style.top = scroller.scrollTop + "px";
  };

  // build recomputes the active heading chain and rebuilds the rows ONLY when it changes
  // (rebuilding every frame is what made the bar flicker). Position is handled by pin().
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
    const sig = shown.map((h) => h.level + ":" + h.text).join("\n");
    if (sig === lastSig) return; // chain unchanged → leave the DOM alone (no flicker)
    lastSig = sig;
    if (!shown.length) {
      bar.style.display = "none";
      bar.textContent = "";
      return;
    }
    bar.style.display = "";
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
    pin(); // reposition synchronously — keeps the bar from lagging behind the scroll
    if (raf) return;
    raf = requestAnimationFrame(() => {
      raf = 0;
      build();
    });
  };
  scroller.addEventListener("scroll", onScroll, { passive: true });
  window.addEventListener("resize", onScroll);
  pin();
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
  btn.title = t("view.copy_this_code");
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
  return resolveMarkdownFileTarget(ref, basePath)?.path || "";
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
// (reveal in FILES). One listing of the parent tells file vs dir vs missing in a single
// request; missing, denied, and unreachable targets are reported instead of doing nothing.
async function openRepoTarget(
  target: { path: string; line?: number; column?: number },
  onOpenFile?: (p: string, line?: number, column?: number, openInNew?: boolean) => void,
  onOpenDir?: (p: string) => void,
  onError?: (message: string) => void,
  openInNew = false,
) {
  let d: { entries?: { name: string; type: string }[] } | null = null;
  try {
    d = await api(`api/fs/tree?path=${encodeURIComponent(dirName(target.path))}`);
  } catch {
    onError?.(t("view.cannot_check_file", { path: target.path }));
    return;
  }
  const entry = (d?.entries || []).find((e) => e.name === baseName(target.path));
  if (!entry) {
    onError?.(t("view.file_not_found", { path: target.path }));
    return;
  }
  if (entry.type === "dir") onOpenDir?.(target.path);
  else if (onOpenFile) onOpenFile(target.path, target.line, target.column, openInNew);
  else onError?.(t("view.cannot_open_from_here", { path: target.path }));
}

// wireLinks classifies and rewires every <a> in the rendered markdown.
function wireLinks(
  el: HTMLElement,
  basePath: string,
  baseDir: string,
  onOpenFile?: (path: string, line?: number, column?: number, openInNew?: boolean) => void,
  onOpenDir?: (path: string) => void,
  onError?: (message: string) => void,
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
    const target = resolveMarkdownFileTarget(href, basePath, baseDir);
    if (target) {
      a.title = target.line
        ? t("view.open_in_pane_at_line", { path: target.path, line: target.line })
        : t("view.open_in_pane", { path: target.path });
      a.setAttribute("aria-label", t("view.open_in_pane_aria", { label: a.textContent || target.path }));
    }
    const openTarget = (openInNew: boolean) => {
      const resolved = resolveMarkdownFileTarget(href, basePath, baseDir);
      if (resolved) openRepoTarget(resolved, onOpenFile, onOpenDir, onError, openInNew);
      else onError?.(t("view.cannot_resolve_link", { href }));
    };
    a.addEventListener("click", (e) => {
      e.preventDefault();
      const mouse = e as MouseEvent;
      openTarget(mouse.ctrlKey || mouse.metaKey);
    });
    a.addEventListener("auxclick", (e) => {
      if (e.button !== 1) return;
      e.preventDefault();
      openTarget(true);
    });
  });
}
