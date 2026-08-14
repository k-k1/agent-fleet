// paneTitle — one human-readable label for what a pane is showing, shared by
// the LayoutMap cells and the pop-out tab's title bar / document.title.
// KIND_JA/jaKind moved here from LayoutMap.tsx so both consumers use one map.
import type { Pane, PaneKind } from "../../layout/types.ts";
import type { Session } from "../../types/session.ts";
import { displayName } from "../../lib/sessionview.ts";
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
  return (meta.title || meta.label || meta.name || tI18n("share.shared_sessions")).replace(/^\[AF\]\s*/, "");
}

/** Title for a pane: the bound session (name · agent) for terminal/mirror
 * panes, else the content's own identity (file name, repo, doc title, …).
 * `sharedMeta` resolves a sharedSession pane's own name — callers that have
 * it (Pane.tsx tab strip, PopoutTitleBar) should pass it in. */
export function paneTitle(pane: Pane, session: Session | null, sharedMeta?: SharedSession): string {
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
      return jaKind("chat");
    case "browser":
      return `127.0.0.1:${c.port}${c.path === "/" ? "" : c.path}`;
    case "browserAttach":
      return jaKind("browserAttach");
    case "sharedSession":
      return sharedSessionLabel(sharedMeta);
  }
}
