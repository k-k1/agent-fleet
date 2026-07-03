import { useMemo } from "react";
import { useApp } from "../state.jsx";
import { paneRows, ordClass, paneCount } from "../lib/panebadge.js";
import { usePaneHover, hoverMatches } from "../lib/panehover.jsx";
import { kindShort, kindLabel, kindClass } from "../lib/sessionkind.js";
import { displayName, stateInfo } from "../lib/sessionview.js";
import type { Session } from "../types/session.ts";

// Short label for a non-session pane (file / scm / doc / diff) shown in a map cell.
const KIND_ABBR: Record<string, string> = { file: "file", scm: "scm", doc: "doc", diff: "diff" };
// Full Japanese name for a non-session pane, used in the cell tooltip / aria-label.
const KIND_JA: Record<string, string> = { file: "ファイル", scm: "ソース管理", doc: "ドキュメント", diff: "差分" };

// LayoutMap draws a schematic of the current split — columns side by side, each a
// stack of its 1–2 panes — so "which session is in which pane" is one glance. Each
// cell shows the pane ordinal (color-matched to its corner chip and Sessions badge),
// a kind hint, and a state dot. Click focuses that pane; hover cross-highlights the
// pane and the Sessions row. Hidden when there's a single pane (nothing to map).
export default function LayoutMap() {
  const { layout, sessions, activePaneId, setActivePane } = useApp();
  const { hover, setHover } = usePaneHover();

  const byName = useMemo(() => new Map((sessions || []).map((s) => [s.name, s] as const)), [sessions]);
  const rows = useMemo(() => paneRows(layout), [layout]);
  if (paneCount(layout) <= 1) return null;

  const ordOf = new Map(rows.map((r) => [r.id, r.ordinal] as const));
  // Cells get narrow once there are 3+ columns, so abbreviate the kind then (cc/cx/…);
  // with 1–2 columns there's room for the full word (claude/shell/…).
  const shortKind = layout.cols.length >= 3;

  return (
    <div className="layoutmap" role="group" aria-label="ペイン配置">
      <div className="lm-cap">レイアウト</div>
      <div className="lm-cols">
        {layout.cols.map((col) => (
        <div className="lm-col" key={col.id}>
          {col.panes.map((p) => {
            const ord = ordOf.get(p.id) ?? 0;
            const s: Session | null = p.kind === "terminal" && p.session ? byName.get(p.session) ?? null : null;
            const st = s ? stateInfo(s) : null;
            const kindTxt = s
              ? shortKind
                ? kindShort(s.kind)
                : kindLabel(s.kind)
              : shortKind
                ? KIND_ABBR[p.kind] || "–"
                : KIND_JA[p.kind] || p.kind;
            // Ordinal keeps the ordinal color (cross-refs the pane corner + Sessions
            // badge); the kind abbrev is tinted by kind so "what agent" reads too.
            const kindCls = s ? " kc-" + kindClass(s.kind) : "";
            const on = hoverMatches(hover, p.id, p.session);
            // Tooltip / aria: "ペイン1: {name} · {kind} · {state}" for a session,
            // or the pane's Japanese view name — so the 2-char cell is legible on
            // hover and to a screen reader without needing a separate legend.
            const label =
              "ペイン" +
              ord +
              ": " +
              (s
                ? `${displayName(s)} · ${kindLabel(s.kind)}${st ? " · " + st.text : ""}`
                : p.kind === "terminal"
                  ? "セッション未接続"
                  : KIND_JA[p.kind] || p.kind);
            return (
              <button
                type="button"
                key={p.id}
                className={
                  "lm-cell " + ordClass(ord) + (p.id === activePaneId ? " active" : "") + (on ? " hover" : "")
                }
                title={label}
                aria-label={label}
                onClick={() => setActivePane(p.id)}
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
