// paneTitle — one human-readable label for what a pane is showing, shared by
// the LayoutMap cells and the pop-out tab's title bar / document.title.
// KIND_JA/jaKind moved here from LayoutMap.tsx so both consumers use one map.
import type { Pane, PaneKind } from "../../layout/types.ts";
import type { Session } from "../../types/session.ts";
import { displayName } from "../../lib/sessionview.ts";
import { kindLabel } from "../../lib/sessionkind.ts";
import { t as tI18n } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";

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

/** Title for a pane: the bound session (name · agent) for terminal/mirror
 * panes, else the content's own identity (file name, repo, doc title, …). */
export function paneTitle(pane: Pane, session: Session | null): string {
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
      return jaKind("sharedSession");
  }
}
