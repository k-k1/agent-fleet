import { useEffect, useRef } from "react";
import DOMPurify from "dompurify";
import hljs from "highlight.js/lib/common";
import { dirName, baseName, isExternalUrl, resolveMarkdownFileTarget, slug } from "../../lib/filemeta.ts";
import { pathRefCandidate, type PathRef } from "../../lib/pathref.ts";
import { resolvePathRefs, type ResolvedPathRef } from "./pathResolve.ts";
// marked: the app's own instance, not the package singleton (see lib/markdown.ts).
import { marked, repairFullwidthTables, splitYamlFrontMatter, type TableRepair } from "../../lib/markdown.ts";
import { api, downloadURL } from "../../core/api/client.ts";
import { useSettings } from "../../lib/settings.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t } from "../../lib/i18n/index.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat, openSessionChatSplit } from "../sessions/open.ts";
import { useChatStore, ensureConvs } from "../chat/store.ts";
import { openChat, openChatSplit } from "../chat/open.ts";
import { openCommit } from "../scm/open.ts";
import { browserAttachmentIdFromLink } from "../../layout/browserAttachmentAction.ts";
import { openBrowserAttachment } from "../browser/attachmentAction.ts";
import { useFilesStore } from "../files/store.ts";

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
  // meaningless without a repo to resolve it in); session / conversation slugs linkify
  // regardless.
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
  // Click on an auto-linked session slug. openInNew is true for Ctrl/Cmd / middle click.
  // Omitted → the default: plain click reuses the active pane (openSessionChat), a modified
  // click opens a new pane. A surface whose own pane must not be replaced (the assistant
  // chat) passes this to force a new pane on every click.
  onOpenSession?: (name: string, openInNew?: boolean) => void;
  // Click on an auto-linked assistant-conversation slug ("a…"). Same contract as
  // onOpenSession: omitted → openChat / openChatSplit defaults; the assistant chat
  // passes this to force a new pane so its own conversation isn't swapped out.
  onOpenConversation?: (id: string, openInNew?: boolean) => void;
  // markRoot tags this rendered block as an anchoring root for transcript marks
  // (docs/69): the highlight layer counts occurrences inside ONE such element, never
  // across the page. Absent → this block cannot carry a mark.
  markRoot?: string;
  // markKind is the transcript part kind behind markRoot ("" = the turn's own text). The
  // highlight layer sends it back when a mark is created; the Agent re-checks it, because
  // only kinds whose text crosses the shared DTO verbatim may carry one (docs/69 §69.4).
  markKind?: string;
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
  onOpenSession,
  onOpenConversation,
  markRoot,
  markKind,
}: MarkdownViewProps) {
  const ref = useRef<HTMLDivElement>(null);
  const toast = useToast();
  const settings = useSettings();
  // Follow the app theme so mermaid diagrams re-render in matching colors.
  const theme = settings.theme === "light" ? "light" : "dark";
  // Initial line-wrap state for each fenced code block's own wrap toggle.
  const codeWrapDefault = settings.markdownCodeWrap;
  // Callers pass inline arrow callbacks (new identity every render), and panes
  // re-render on hover/focus — keep the latest callbacks in refs so the render
  // effect doesn't depend on them. Otherwise every pane hover re-parsed the whole
  // document and re-rendered mermaid async, making the preview width flap.
  const onOpenFileRef = useRef(onOpenFile);
  const onOpenDirRef = useRef(onOpenDir);
  const onOpenSessionRef = useRef(onOpenSession);
  const onOpenConversationRef = useRef(onOpenConversation);
  onOpenFileRef.current = onOpenFile;
  onOpenDirRef.current = onOpenDir;
  onOpenSessionRef.current = onOpenSession;
  onOpenConversationRef.current = onOpenConversation;

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    let alive = true;

    const frontMatter = splitYamlFrontMatter(source ?? "");
    const body = frontMatter?.body ?? source ?? "";
    // Tables typed with a fullwidth ｜ parse as one run-on paragraph; repair them so the
    // document is at least readable here, and mark what was repaired below.
    const repair = repairFullwidthTables(body);
    const rawHtml = marked.parse(repair?.body ?? body, { gfm: true, breaks }) as string;
    el.innerHTML = DOMPurify.sanitize(rawHtml);
    if (frontMatter) renderFrontMatter(el, frontMatter.attributes, frontMatter.lenient);

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

    if (repair) markRepairedTables(el, repair);

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

    // Auto-link bare git commit hashes (→ commit pane), session slugs (→ that
    // session's mirror) and assistant-conversation slugs (→ that chat pane) mentioned
    // in prose. Runs AFTER wireLinks so the anchors it creates (no href) aren't
    // reclassified as file links; skips code / pre / existing anchors like renderEmoji
    // does. Slugs only link when the session / conversation actually exists; a hash
    // links optimistically and is verified against the repo on click.
    const runLinkify = () =>
      linkifyRefs(
        el,
        repo,
        (message) => toast(message),
        (name, openInNew) => {
          const cb = onOpenSessionRef.current;
          if (cb) cb(name, openInNew);
          else if (openInNew) openSessionChatSplit(name);
          else openSessionChat(name);
        },
        (id, openInNew) => {
          const cb = onOpenConversationRef.current;
          if (cb) cb(id, openInNew);
          else if (openInNew) openChatSplit(id);
          else openChat(id);
        },
      );
    runLinkify();
    // A conv slug can only be existence-checked once the conversation list is in the
    // store. When this document mentions one before any surface has loaded the list
    // (e.g. a mirror opened straight from a deep link, left rail not mounted yet),
    // fetch it once and re-run the linkifier — idempotent: existing anchors are skipped.
    if (useChatStore.getState().convs === null && CONV_HINT_RE.test(source ?? "")) {
      void ensureConvs().then(() => {
        if (alive) runLinkify();
      });
    }

    // Link the file paths written as inline code (`docs/65-drawio-viewer.md`) to the file
    // they name — but only on a surface that can actually open one. onOpenFile absent means
    // the shared view (docs/59): a recipient has no such file, so nothing is linked there
    // rather than shown a link that opens nothing. The paths resolve against the reply's
    // own working directory (the mirror passes the turn's cwd), or — in a document viewer,
    // which has no cwd — against the folder the document itself sits in.
    if (onOpenFileRef.current) {
      void linkifyPathRefs(
        el,
        baseDir || dirName(basePath),
        () => alive,
        (p, line, column, openInNew) => onOpenFileRef.current?.(p, line, column, openInNew),
        // No onOpenDir on this surface (the mirror passes only onOpenFile) → fall back to
        // revealing the directory in the ファイル rail, which is what the Doc viewer's own
        // onOpenDir does. focus: the reader clicked to GO there, so the rail takes the
        // keyboard too. Safe as a default precisely because this whole pass is gated on
        // onOpenFile above: it can never fire on somebody else's shared session.
        (p) =>
          onOpenDirRef.current
            ? onOpenDirRef.current(p)
            : useFilesStore.getState().revealInFiles(p, { focus: true }),
        (message) => toast(message),
      );
    }

    // VS Code-style sticky headings: when this markdown is inside a scroll container
    // (.md-scroll — the Doc / File viewers, not chat bubbles), pin the heading path of
    // the current scroll position at the top. Purely imperative since the body is
    // rendered as innerHTML. Cleaned up when the effect re-runs / unmounts.
    let stickyCleanup = () => {};
    const scroller = el.closest<HTMLElement>(".md-scroll");
    if (scroller) stickyCleanup = setupStickyHeadings(el, scroller);

    // Syntax-highlight every fenced code block except mermaid (handled below), and
    // give each one copy and line-wrap controls at its bottom-right.
    el.querySelectorAll<HTMLElement>("pre > code").forEach((code) => {
      if (code.classList.contains("language-mermaid")) return;
      try {
        hljs.highlightElement(code);
      } catch {}
      addCopyButton(code, codeWrapDefault);
    });
    el.querySelectorAll<HTMLElement>("blockquote").forEach(addQuoteCopyButton);

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
  }, [source, basePath, baseDir, repo, breaks, streaming, theme, codeWrapDefault, toast]);

  return (
    <div
      className="markdown"
      ref={ref}
      data-mark-root={markRoot || undefined}
      data-mark-kind={markRoot ? markKind || "" : undefined}
    />
  );
}

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
function markRepairedTables(root: HTMLElement, repair: TableRepair) {
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

function renderFrontMatter(root: HTMLElement, attributes: Record<string, unknown>, lenient?: boolean) {
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

// linkifyRefs turns bare git commit hashes, session slugs and assistant-conversation
// slugs into clickable links, mirroring renderEmoji's text-node walk (skips existing
// anchors so we never double-link). Matches are scanned in one combined pass so the
// token shapes can't overlap or fight over the same run of text.
//
// - commit hash: 7–40 hex chars. Only linkified when a `repo` is known (a hash is
//   unresolvable without one). Linked optimistically; the click handler verifies the sha
//   exists in the repo before opening the commit pane, and toasts otherwise — so a hex
//   token that isn't actually a commit does nothing but tell you so ("存在しない→リンクにしない").
// - session slug: `s` + 6 lowercase base32 chars (Session.name — the shape minted by the
//   agent's randSlug, e.g. "sukbq4s"). Only linkified when a session with that exact name
//   currently exists (looked up live in the sessions store); a stale/unknown slug is left
//   as plain text (so a 7-char English s-word that isn't a live session never links).
// - conversation slug: `a` + 6 lowercase base32 chars (Conversation.Slug — the assistant
//   twin minted by randConvSlug, e.g. "azw7wys"). Existence-gated against the chat
//   store's conversation list, so an English a-word ("against", "already") never links.
//   Checked BEFORE the commit shape: an all-hex token like "abcdef2" is both a valid
//   sha prefix and a valid conv slug, and a live conversation is the stronger signal —
//   the commit branch keeps the token only when no such conversation exists.
//
// Code context: a fenced block (<pre>) is literal source and is never touched. INLINE
// code (`sukbq4s` / `9219ab9` written in backticks — the common way these are mentioned)
// IS linkified for all shapes: a slug in backticks references that session/conversation,
// and a hash in backticks references that commit (commit still click-verified).
const COMMIT_RE = "[0-9a-f]{7,40}";
const SLUG_RE = "s[a-z2-7]{6}"; // randSlug: "s" + 6 base32-lower chars (a-z, 2-7)
const CONV_RE = "a[a-z2-7]{6}"; // randConvSlug: "a" + 6 base32-lower chars
// Combined; \b keeps us off sub-word matches. A session slug starts with "s" (non-hex)
// so it never collides; "a" IS hex, so a rare all-hex conv slug is disambiguated by the
// existence-gated classification order above, not by the regex.
const REF_RE = new RegExp(`\\b(?:${COMMIT_RE}|${SLUG_RE}|${CONV_RE})\\b`, "g");
// Cheap "does this document mention a conv-slug-shaped token at all" probe, used to
// decide whether loading the conversation list is worth it (see ensureConvs call).
const CONV_HINT_RE = new RegExp(`\\b${CONV_RE}\\b`);

function linkifyRefs(
  root: HTMLElement,
  repo: string | null,
  onError: (message: string) => void,
  openSession: (name: string, openInNew: boolean) => void,
  openConversation: (id: string, openInNew: boolean) => void,
) {
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
    REF_RE.lastIndex = 0;
    let m = REF_RE.exec(text);
    if (!m) continue;

    const out = document.createDocumentFragment();
    let last = 0;
    do {
      const token = m[0];
      let a: HTMLAnchorElement | null = null;
      // conv-slug shape first (see the classification-order note above): link only if
      // a conversation with that slug exists right now.
      if (/^a[a-z2-7]{6}$/.test(token)) {
        const convs = useChatStore.getState().convs;
        if (convs?.some((c) => c.slug === token)) a = makeConversationLink(token, onError, openConversation);
      }
      if (!a && /^[0-9a-f]{7,40}$/.test(token)) {
        if (repo) a = makeCommitLink(token, repo, onError);
      } else if (!a && /^s[a-z2-7]{6}$/.test(token)) {
        // session-slug shape: link only if that session exists right now
        const exists = useSessionsStore.getState().sessions.some((s) => s.name === token);
        if (exists) a = makeSessionLink(token, openSession);
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

// makeConversationLink builds a non-navigating anchor that opens an assistant
// conversation's chat pane. The slug is re-resolved against the chat store at click
// time (not captured at render) so a conversation deleted in between toasts instead
// of opening a dead pane. Modifier keys follow the session-link convention.
function makeConversationLink(
  slugText: string,
  onError: (message: string) => void,
  openConversation: (id: string, openInNew: boolean) => void,
): HTMLAnchorElement {
  const a = document.createElement("a");
  a.className = "md-ref-link md-conv-link";
  a.textContent = slugText;
  a.setAttribute("role", "link");
  a.tabIndex = 0;
  a.title = t("view.open_conversation", { slug: slugText });
  const open = (openInNew: boolean) => {
    const conv = useChatStore.getState().convs?.find((c) => c.slug === slugText);
    if (!conv) {
      onError(t("view.conversation_not_found", { slug: slugText }));
      return;
    }
    openConversation(conv.id, openInNew);
  };
  a.addEventListener("click", (e) => {
    e.preventDefault();
    open(e.ctrlKey || e.metaKey);
  });
  a.addEventListener("auxclick", (e) => {
    if (e.button !== 1) return; // middle click → new pane
    e.preventDefault();
    open(true);
  });
  a.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      open(e.ctrlKey || e.metaKey);
    }
  });
  return a;
}

// makeSessionLink builds a non-navigating anchor that opens a session's chat mirror.
// Modifier keys follow the same convention as file links (wireLinks): a plain click / Enter
// is the default open, while Ctrl/Cmd-click and a middle click force a new pane (openInNew).
function makeSessionLink(name: string, openSession: (name: string, openInNew: boolean) => void): HTMLAnchorElement {
  const a = document.createElement("a");
  a.className = "md-ref-link md-session-link";
  a.textContent = name;
  a.setAttribute("role", "link");
  a.tabIndex = 0;
  a.title = t("view.open_session", { name });
  a.addEventListener("click", (e) => {
    e.preventDefault();
    openSession(name, e.ctrlKey || e.metaKey);
  });
  a.addEventListener("auxclick", (e) => {
    if (e.button !== 1) return; // middle click → new pane
    e.preventDefault();
    openSession(name, true);
  });
  a.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      openSession(name, e.ctrlKey || e.metaKey);
    }
  });
  return a;
}

// linkifyPathRefs turns the file paths an agent writes as INLINE CODE — `docs/65.md`,
// `console/src/lib/filemeta.ts:73`, `_act-parts/` — into links that open that file in a
// pane (a directory reveals in the ファイル rail). Until now those were dead text: the
// coordinate you most want to follow was the one thing you had to retype.
//
// Existence-gated, like a session slug and unlike a commit hash: a path is linked only
// when it is really there. That is what keeps it quiet — a generic `package.json` in
// prose, a `--flag` shaped like a name, a file from another machine's log all stay plain
// text instead of becoming links that answer a click with "見つかりません".
//
// WHICH file a written path names is not decided here. The Console can only guess (it has
// a cwd string and a regex for "repos/<name>"), while the agent knows the working copy's
// real root, what lives outside home, and what is denylisted — so it resolves cwd-first,
// repository-root-second, and answers every path in the message in one request
// (pathResolve.ts → workspace/agent/fs_resolve.go). That fallback matters: an agent
// launched in a subfolder routinely writes paths from the repository root.
//
// Cost is bounded on three sides: pathRefCandidate rejects anything not path-shaped before
// a request exists, the answers and the in-flight requests are memoized per (cwd, ref)
// across turns, and a single document asks about at most MAX_REFS_PER_DOC paths — a reply
// citing a hundred of them links the first few and leaves the rest as text.
//
// Fenced blocks (<pre>) are left alone, as everywhere else in this file: a code sample is
// literal source, and its lines are usually commands rather than citations.
const MAX_REFS_PER_DOC = 32;

async function linkifyPathRefs(
  el: HTMLElement,
  cwd: string,
  alive: () => boolean,
  onOpenFile: (path: string, line?: number, column?: number, openInNew?: boolean) => void,
  onOpenDir: (path: string) => void,
  onError: (message: string) => void,
) {
  const candidates: { code: HTMLElement; ref: PathRef }[] = [];
  el.querySelectorAll<HTMLElement>("code").forEach((code) => {
    if (candidates.length >= MAX_REFS_PER_DOC) return;
    if (code.dataset.pathLink) return; // already processed (the effect can link twice)
    if (code.closest("pre,a")) return; // fenced source, or already inside a link
    if (code.childElementCount) return; // not a bare token
    const ref = pathRefCandidate(code.textContent);
    if (ref) candidates.push({ code, ref });
  });
  if (!candidates.length) return;

  const resolved = await resolvePathRefs(
    cwd,
    candidates.map((c) => c.ref.ref),
  );
  if (!alive()) return; // the effect re-ran or the view unmounted while we were asking

  for (const { code, ref } of candidates) {
    const hit = resolved.get(ref.ref);
    if (!hit) continue; // no such file — leave the text exactly as the agent wrote it
    // A directory opens by revealing it in the ファイル rail, and that tree is rooted at
    // home — so a directory OUTSIDE it (the scratch base, the staged docs mount, both of
    // which the resolver can legitimately place) has nowhere to be revealed. Leave it as
    // text rather than offer a link that scrolls to nothing. Files there open fine.
    if (hit.type === "dir" && hit.path.startsWith("/")) continue;
    if (!code.isConnected || code.dataset.pathLink) continue;
    code.dataset.pathLink = "1";
    const a = makePathLink(cwd, ref, hit, onOpenFile, onOpenDir, onError);
    while (code.firstChild) a.appendChild(code.firstChild);
    code.appendChild(a);
  }
}

// makePathLink builds the non-navigating anchor a linked path gets. The click re-resolves
// (bypassing the memo) rather than trusting what was true when the message rendered, so a
// file deleted or moved in between says so instead of opening an empty pane. Modifier keys
// follow the file-link convention (wireLinks): Ctrl/Cmd or middle click forces a new pane.
function makePathLink(
  cwd: string,
  ref: PathRef,
  hit: ResolvedPathRef,
  onOpenFile: (path: string, line?: number, column?: number, openInNew?: boolean) => void,
  onOpenDir: (path: string) => void,
  onError: (message: string) => void,
): HTMLAnchorElement {
  const a = document.createElement("a");
  a.className = "md-ref-link md-path-link";
  a.setAttribute("role", "link");
  a.tabIndex = 0;
  a.title =
    hit.type === "dir"
      ? t("view.reveal_in_files", { path: hit.path })
      : ref.line
        ? t("view.open_in_pane_at_line", { path: hit.path, line: ref.line })
        : t("view.open_in_pane", { path: hit.path });
  const open = async (openInNew: boolean) => {
    const fresh = (await resolvePathRefs(cwd, [ref.ref], { fresh: true })).get(ref.ref);
    if (!fresh) {
      onError(t("view.file_not_found", { path: hit.path }));
      return;
    }
    if (fresh.type !== "dir") onOpenFile(fresh.path, ref.line, ref.column, openInNew);
    // Moved out from under home since it was linked (or re-resolved to a directory that
    // never was under it): the rail cannot show it, so say so instead of doing nothing.
    else if (fresh.path.startsWith("/")) onError(t("view.cannot_open_from_here", { path: fresh.path }));
    else onOpenDir(fresh.path);
  };
  a.addEventListener("click", (e) => {
    e.preventDefault();
    void open(e.ctrlKey || e.metaKey);
  });
  a.addEventListener("auxclick", (e) => {
    if (e.button !== 1) return; // middle click → new pane
    e.preventDefault();
    void open(true);
  });
  a.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      void open(e.ctrlKey || e.metaKey);
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

// addCopyButton pins code actions at the bottom-right of a fenced code block: copy
// copies exactly that block, while wrap toggles its own line wrapping. Imperative
// because the markdown is rendered as sanitized innerHTML, not React nodes.
function addCopyButton(code: HTMLElement, wrapDefault: boolean) {
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
function addQuoteCopyButton(quote: HTMLElement) {
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

    // The Chromium attachment action link (docs/53 §53.7). It has to be claimed
    // BEFORE the repo-file branch below: `/open/browser-attachment/<id>` carries
    // no scheme, so the file resolver would happily read it as a repo-root path
    // and answer the click with "file not found" — which is exactly how this
    // link died in the mirror. Opening the pane here also spares the user a full
    // page navigation; the action ROUTE stays for new tabs and reloads.
    const attachmentId = browserAttachmentIdFromLink(href);
    if (attachmentId) {
      a.classList.add("action-link");
      a.title = t("browser.attach.open_link");
      a.addEventListener("click", (e) => {
        e.preventDefault();
        void openBrowserAttachment(attachmentId);
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
