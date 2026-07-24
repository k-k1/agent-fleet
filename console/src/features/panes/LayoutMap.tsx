// LayoutMap — schematic of the current split (columns side by side, each a
// stack of 1–2 panes) so "which session is in which pane" is one glance. Each
// cell: pane ordinal (color-matched to corner chip + rail badge), a kind hint,
// a state dot. Click focuses; hover cross-highlights. Hidden when unsplit.
// Ported onto the new layout types (pane.session + content union).
import { useMemo } from "react";
import { useLayoutStore } from "../../layout/store.ts";
import { paneRows, ordClass, paneCount } from "../../layout/badges.ts";
import { usePaneHover, hoverMatches } from "../../lib/panehover.tsx";
import { kindShort, kindLabel, kindClass } from "../../lib/sessionkind.ts";
import { displayName, stateInfo } from "../../lib/sessionview.ts";
import { useSessionsStore } from "../sessions/store.ts";
import type { Session } from "../../types/session.ts";
import type { PaneKind } from "../../layout/types.ts";
import { useT } from "../../lib/i18n/index.ts";
import { jaKind } from "./paneTitle.ts";

// Short label for a non-session pane shown in a narrow map cell. The localized
// long name (jaKind / KIND_JA) lives in paneTitle.ts, shared with the pop-out
// title bar.
const KIND_ABBR: Partial<Record<PaneKind, string>> = {
  file: "file",
  scm: "scm",
  changes: "chg",
  commit: "cmt",
  wtdiff: "diff",
  doc: "doc",
  diff: "diff",
  chat: "chat",
};

export function LayoutMap() {
  const layout = useLayoutStore((s) => s.layout);
  const setActive = useLayoutStore((s) => s.setActive);
  const sessions = useSessionsStore((s) => s.sessions);
  const { hover, setHover } = usePaneHover();
  const tr = useT();

  const byName = useMemo(() => new Map(sessions.map((s) => [s.name, s] as const)), [sessions]);
  const rows = useMemo(() => paneRows(layout), [layout]);
  if (paneCount(layout) <= 1) return null;

  const ordOf = new Map(rows.map((r) => [r.id, r.ordinal] as const));
  // Cells get narrow with 3+ columns — abbreviate the kind then.
  const shortKind = layout.cols.length >= 3;

  return (
    <div className="layoutmap" role="group" aria-label={tr("pane.map_aria")}>
      <div className="lm-cap">{tr("pane.layout")}</div>
      <div className="lm-cols">
        {layout.cols.map((col) => (
          <div className="lm-col" key={col.id}>
            {col.panes.map((p) => {
              const ord = ordOf.get(p.id) ?? 0;
              const isTerm = p.content.kind === "terminal";
              const s: Session | null = isTerm && p.session ? (byName.get(p.session) ?? null) : null;
              const st = s ? stateInfo(s) : null;
              const empty = isTerm && !p.session;
              const kindTxt = s
                ? shortKind
                  ? kindShort(s.kind)
                  : kindLabel(s.kind)
                : empty
                  ? "empty"
                  : shortKind
                    ? KIND_ABBR[p.content.kind] || "–"
                    : jaKind(p.content.kind);
              const kindCls = s ? " kc-" + kindClass(s.kind) : "";
              const on = hoverMatches(hover, p.id, p.session);
              const label =
                tr("pane.pane_n", { ord }) +
                ": " +
                (s
                  ? `${displayName(s)} · ${kindLabel(s.kind)}${st ? " · " + st.text : ""}`
                  : isTerm
                    ? tr("pane.no_session")
                    : jaKind(p.content.kind));
              return (
                <button
                  type="button"
                  key={p.id}
                  className={
                    "lm-cell " + ordClass(ord) + (p.id === layout.activeId ? " active" : "") + (on ? " hover" : "")
                  }
                  title={label}
                  aria-label={label}
                  onClick={() => setActive(p.id)}
                  onMouseEnter={() => setHover({ session: p.session || null, paneId: p.id })}
                  onMouseLeave={() => setHover(null)}
                >
                  <span className="lm-ord">{ord}</span>
                  <span className={"lm-kind" + kindCls}>{kindTxt}</span>
                  {st && <span className={"lm-dot " + st.cls} />}
                </button>
              );
            })}
          </div>
        ))}
      </div>
    </div>
  );
}
