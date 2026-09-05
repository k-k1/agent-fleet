// Turns bare commit hashes, session slugs, assistant-conversation slugs and file paths
// written as inline code into links that open the corresponding surface.
import { pathRefCandidate, type PathRef } from "../../../lib/pathref.ts";
import { resolvePathRefs, type ResolvedPathRef } from "../pathResolve.ts";
import { api } from "../../../core/api/client.ts";
import { t } from "../../../lib/i18n/index.ts";
import { useSessionsStore } from "../../sessions/store.ts";
import { useChatStore } from "../../chat/store.ts";
import { openCommit } from "../../scm/open.ts";

// linkifyRefs turns bare git commit hashes, session slugs and assistant-conversation
// slugs into clickable links, mirroring renderEmoji's text-node walk (skips existing
// anchors so we never double-link). Matches are scanned in one combined pass so the
// token shapes can't overlap or fight over the same run of text.
//
// - commit hash: 7–40 hex chars. Only linkified when a `repo` is known (a hash is
//   unresolvable without one). Linked optimistically; the click handler verifies the sha
//   exists in the repo before opening the commit pane, and toasts otherwise — so a hex
//   token that isn't actually a commit does nothing but tell you so (it is not silently
//   suppressed at render time).
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
export const CONV_HINT_RE = new RegExp(`\\b${CONV_RE}\\b`);

export function linkifyRefs(
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

// linkifyPathRefs turns the file paths an agent writes as INLINE CODE — `docs/log/65.md`,
// `console/src/lib/filemeta.ts:73`, `_act-parts/` — into links that open that file in a
// pane (a directory reveals in the Files rail). Otherwise those are dead text: the
// coordinate you most want to follow is the one thing you have to retype.
//
// Existence-gated, like a session slug and unlike a commit hash: a path is linked only
// when it is really there. That is what keeps it quiet — a generic `package.json` in
// prose, a `--flag` shaped like a name, a file from another machine's log all stay plain
// text instead of becoming links that answer a click with "not found".
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

export async function linkifyPathRefs(
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
    // A directory opens by revealing it in the Files rail, and that tree is rooted at
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
