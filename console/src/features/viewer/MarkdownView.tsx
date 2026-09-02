import { useEffect, useRef } from "react";
import DOMPurify from "dompurify";
import hljs from "highlight.js/lib/common";
import { dirName, slug } from "../../lib/filemeta.ts";
// marked: the app's own instance, not the package singleton (see lib/markdown.ts).
import { marked, repairFullwidthTables, splitYamlFrontMatter } from "../../lib/markdown.ts";
import { useSettings } from "../../lib/settings.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { openSessionChat, openSessionChatSplit } from "../sessions/open.ts";
import { useChatStore, ensureConvs } from "../chat/store.ts";
import { openChat, openChatSplit } from "../chat/open.ts";
import { useFilesStore } from "../files/store.ts";
import { markRepairedTables, renderFrontMatter } from "./parts/mdFrontMatter.ts";
import { renderEmoji } from "./parts/mdEmoji.ts";
import { CONV_HINT_RE, linkifyPathRefs, linkifyRefs } from "./parts/mdRefLinks.ts";
import { setupStickyHeadings } from "./parts/mdStickyHeadings.ts";
import { addCopyButton, addQuoteCopyButton } from "./parts/mdCopyButtons.ts";
import { wireImages, wireLinks } from "./parts/mdLinks.ts";

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
  // (docs/log/69): the highlight layer counts occurrences inside ONE such element, never
  // across the page. Absent → this block cannot carry a mark.
  markRoot?: string;
  // markKind is the transcript part kind behind markRoot ("" = the turn's own text). The
  // highlight layer sends it back when a mark is created; the Agent re-checks it, because
  // only kinds whose text crosses the shared DTO verbatim may carry one (docs/log/69 §69.4).
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

    // Link the file paths written as inline code (`docs/log/65-drawio-viewer.md`) to the file
    // they name — but only on a surface that can actually open one. onOpenFile absent means
    // the shared view (docs/log/59): a recipient has no such file, so nothing is linked there
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
