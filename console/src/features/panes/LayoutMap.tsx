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
import { selectedView } from "../../layout/ops.ts";

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
  browser: "web",
  browserAttach: "web",
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
            {col.cells.map((cell) => {
              const p = selectedView(cell);
              const ord = ordOf.get(cell.id) ?? 0;
              const isTerm = p?.content.kind === "terminal";
              const s: Session | null = isTerm && p?.session ? (byName.get(p.session) ?? null) : null;
              const st = s ? stateInfo(s) : null;
              const empty = !p;
              const kindTxt = s
                ? shortKind
                  ? kindShort(s.kind)
                  : kindLabel(s.kind)
                : empty
                  ? tr("pane.empty")
                  : shortKind
                    ? (p ? KIND_ABBR[p.content.kind] || "–" : "–")
                    : (p ? jaKind(p.content.kind) : tr("pane.empty"));
              const kindCls = s ? " kc-" + kindClass(s.kind) : "";
              const on = hoverMatches(hover, cell.id, p?.session || null);
              const label =
                tr("pane.pane_n", { ord }) +
                ": " +
                (s
                  ? `${displayName(s)} · ${kindLabel(s.kind)}${st ? " · " + st.text : ""}`
                  : !p || isTerm
                    ? tr("pane.no_session")
                    : jaKind(p.content.kind));
              return (
                <button
                  type="button"
                  key={cell.id}
                  className={
                    "lm-cell " + ordClass(ord) + (cell.id === layout.activeCellId ? " active" : "") + (on ? " hover" : "")
                  }
                  title={label}
                  aria-label={label}
                  onClick={() => setActive(cell.id)}
                  onMouseEnter={() => setHover({ session: p?.session || null, paneId: cell.id })}
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
