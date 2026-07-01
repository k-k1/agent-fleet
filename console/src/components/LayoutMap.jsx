import { useMemo } from "react";
import { useApp } from "../state.jsx";
import { paneRows, ordClass, paneCount } from "../lib/panebadge.js";
import { usePaneHover, hoverMatches } from "../lib/panehover.jsx";
import { kindShort } from "../lib/sessionkind.js";
import { stateInfo } from "../lib/sessionview.js";

// Short label for a non-session pane (file / scm / doc / diff) shown in a map cell.
const KIND_ABBR = { file: "file", scm: "scm", doc: "doc", diff: "diff" };

// LayoutMap draws a schematic of the current split — columns side by side, each a
// stack of its 1–2 panes — so "which session is in which pane" is one glance. Each
// cell shows the pane ordinal (color-matched to its corner chip and Sessions badge),
// a kind hint, and a state dot. Click focuses that pane; hover cross-highlights the
// pane and the Sessions row. Hidden when there's a single pane (nothing to map).
export default function LayoutMap() {
  const { layout, sessions, activePaneId, setActivePane } = useApp();
  const { hover, setHover } = usePaneHover();

  const byName = useMemo(() => new Map((sessions || []).map((s) => [s.name, s])), [sessions]);
  const rows = useMemo(() => paneRows(layout), [layout]);
  if (paneCount(layout) <= 1) return null;

  const ordOf = new Map(rows.map((r) => [r.id, r.ordinal]));

  return (
    <div className="layoutmap" role="group" aria-label="ペイン配置">
      {layout.cols.map((col) => (
        <div className="lm-col" key={col.id}>
          {col.panes.map((p) => {
            const ord = ordOf.get(p.id);
            const s = p.kind === "terminal" && p.session ? byName.get(p.session) : null;
            const st = s ? stateInfo(s) : null;
            const kindTxt = s ? kindShort(s.kind) : KIND_ABBR[p.kind] || "–";
            const on = hoverMatches(hover, p.id, p.session);
            return (
              <button
                type="button"
                key={p.id}
                className={
                  "lm-cell " + ordClass(ord) + (p.id === activePaneId ? " active" : "") + (on ? " hover" : "")
                }
                title={s ? s.name : p.kind === "terminal" ? "セッション未接続" : KIND_ABBR[p.kind] || p.kind}
                onClick={() => setActivePane(p.id)}
                onMouseEnter={() => setHover({ session: p.session || null, paneId: p.id })}
                onMouseLeave={() => setHover(null)}
              >
                <span className="lm-ord">{ord}</span>
                <span className="lm-kind">{kindTxt}</span>
                {st && <span className={"lm-dot " + st.cls} />}
              </button>
            );
          })}
        </div>
      ))}
    </div>
  );
}
