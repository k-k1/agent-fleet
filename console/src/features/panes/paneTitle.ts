// paneTitle — one human-readable label for what a pane is showing, shared by
// the LayoutMap cells and the pop-out tab's title bar / document.title.
// KIND_JA/jaKind moved here from LayoutMap.tsx so both consumers use one map.
import type { Pane, PaneKind } from "../../layout/types.ts";
import type { Session } from "../../types/session.ts";
import { displayName, stripLabelTag } from "../../lib/sessionview.ts";
import { kindLabel } from "../../lib/sessionkind.ts";
import { t as tI18n } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import type { SharedSession } from "../sharing/store.ts";

export const KIND_JA: Partial<Record<PaneKind, MsgKey>> = {
  file: "pane.kind.file",
  scm: "pane.kind.scm",
  changes: "pane.kind.changes",
  commit: "pane.kind.commit",
  wtdiff: "pane.kind.wtdiff",
  doc: "pane.kind.doc",
  diff: "pane.kind.diff",
  chat: "pane.kind.chat",
  read: "pane.kind.read",
  browser: "pane.kind.browser",
  browserAttach: "pane.kind.browser_attach",
  sharedSession: "share.shared_sessions",
};

// Resolve a non-session pane kind to its localized label (falls back to the raw kind).
export function jaKind(k: PaneKind): string {
  const key = KIND_JA[k];
  return key ? tI18n(key) : k;
}

const basename = (p: string): string => p.split("/").filter(Boolean).pop() || p;

/** Display name for a shared-session tab: the session's own name, so several
 * shared-session tabs read apart instead of all showing the generic kind
 * label (falls back to it while the shared-sessions list hasn't loaded yet). */
export function sharedSessionLabel(meta: SharedSession | undefined): string {
  if (!meta) return tI18n("share.shared_sessions");
  return stripLabelTag(meta.title || meta.label || meta.name || tI18n("share.shared_sessions"));
}

/** Display name for a chat tab: the conversation's own title, so several chat
 * tabs read apart instead of all showing the generic kind label (falls back to
 * it for a draft that isn't a conversation yet, or before the title is known). */
export function chatLabel(title: string | undefined): string {
  return title?.trim() || tI18n("pane.kind.chat");
}

/** Identities `paneTitle` can't derive from the pane alone, resolved by the caller
 * from the relevant store: the recipient-side name of a shared session, the title
 * of an assistant conversation. Absent members just fall back to the kind label. */
export interface PaneTitleMeta {
  shared?: SharedSession;
  chatTitle?: string;
}

/** Title for a pane: the bound session (name · agent) for terminal/mirror
 * panes, else the content's own identity (file name, repo, doc title, …).
 * `meta` resolves the identities that live outside the pane — callers that have
 * them (Pane.tsx tab strip, PopoutTitleBar) should pass them in. */
export function paneTitle(pane: Pane, session: Session | null, meta: PaneTitleMeta = {}): string {
  const c = pane.content;
  switch (c.kind) {
    case "terminal":
      if (session) return `${displayName(session)} · ${kindLabel(session.kind)}`;
      return pane.session || tI18n("pane.no_session");
    case "file":
    case "read":
      return basename(c.filePath);
    case "wtdiff":
      return `${basename(c.filePath)} · ${jaKind("wtdiff")}`;
    case "scm":
    case "changes":
      return `${c.scmRepo} · ${jaKind(c.kind)}`;
    case "commit":
      return `${c.scmRepo} · ${c.commitSha.slice(0, 7)}`;
    case "doc":
    case "diff":
      return c.docTitle;
    case "chat":
      return chatLabel(meta.chatTitle);
    case "browser":
      return `127.0.0.1:${c.port}${c.path === "/" ? "" : c.path}`;
    case "browserAttach":
      return jaKind("browserAttach");
    case "sharedSession":
      return sharedSessionLabel(meta.shared);
  }
}
