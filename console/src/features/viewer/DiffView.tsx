import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { useSettings, fontStack } from "../../lib/settings.ts";
import { baseName } from "../../lib/filemeta.ts";
import { useT } from "../../lib/i18n/index.ts";
import type { CSSProperties, ReactNode } from "react";

// DiffView renders the before/after of an edit-family tool (Edit/Write/MultiEdit) as a
// line-level diff in its own pane. The edits live in the pane descriptor (diffEdits),
// captured from the transcript — no file on disk is read, so it shows exactly what that
// tool call changed (a Write is all-added; an Edit shows its changed region in context).

// One captured edit: the tool's before/after text for a region.
export interface DiffEdit {
  old?: string;
  new?: string;
}

interface DiffViewProps {
  title?: string;
  tool?: string;
  edits?: DiffEdit[];
  wrap?: boolean;
  /** Pane popout/wrap/close (tabbed-grid mode only — see Pane.tsx tabHeaderActions). */
  headerActions?: ReactNode;
}

// A rendered diff row: context / addition / deletion, with old/new line numbers.
// Exported for the editor's AI-suggestion review panel (docs/44 Phase 4), which
// renders the selection → replacement pair through the same lineDiff.
export interface DiffRow {
  t: "ctx" | "add" | "del";
  text: string;
  o?: number;
  n?: number;
}

export function DiffView({ title, tool, edits, wrap, headerActions }: DiffViewProps) {
  const tr = useT();
  const settings = useSettings();
  const viewerStyle = {
    "--viewer-font": fontStack(settings.viewerFont),
    "--viewer-size": settings.viewerSize + "px",
  } as CSSProperties;
  const list = edits || [];
  // Aggregate +/- counts across all hunks for the header summary.
  let added = 0,
    removed = 0;
  const hunks = list.map((e) => {
    const rows = lineDiff(e.old || "", e.new || "");
    for (const r of rows) {
      if (r.t === "add") added++;
      else if (r.t === "del") removed++;
    }
    return rows;
  });
  return (
    <div className={"fileview diffview" + (wrap ? "" : " nowrap")} style={viewerStyle}>
      <ViewHead className="fileinfo" actions={headerActions}>
        <span className="fi-name mono" title={title}>
          <Icon name="diff" /> {baseName(title || "") || title || tr("view.diff")}
        </span>
        <span className="dv-stat">
          {tool && <span className="fi-tag">{tool}</span>}
          {added > 0 && <span className="dv-add">+{added}</span>}
          {removed > 0 && <span className="dv-del">−{removed}</span>}
        </span>
      </ViewHead>
      <div className="dv-scroll">
        {hunks.map((rows, hi) => (
          <div className="dv-hunk" key={hi}>
            {list.length > 1 && <div className="dv-hunk-head">{tr("view.change")} {hi + 1}</div>}
            <table className="dv-table">
              <tbody>
                {rows.map((r, i) => (
                  <tr className={"dv-row dv-" + r.t} key={i}>
                    <td className="dv-gutter">{r.o || ""}</td>
                    <td className="dv-gutter">{r.n || ""}</td>
                    <td className="dv-mark">{r.t === "add" ? "+" : r.t === "del" ? "−" : " "}</td>
                    <td className="dv-code">{r.text === "" ? " " : r.text}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ))}
      </div>
    </div>
  );
}

// lineDiff computes a line-level diff of two strings via LCS, returning rows tagged
// ctx|add|del with old/new line numbers. Empty sides (a Write's old="") produce
// all-added rows. A size guard falls back to a plain remove-then-add for pathologically
// large blocks so the DP table can't blow up.
export function lineDiff(oldStr: string, newStr: string): DiffRow[] {
  const a = oldStr === "" ? [] : oldStr.replace(/\n$/, "").split("\n");
  const b = newStr === "" ? [] : newStr.replace(/\n$/, "").split("\n");
  const n = a.length,
    m = b.length;
  const rows: DiffRow[] = [];
  if (n === 0 || m === 0 || n * m > 4_000_000) {
    let o = 1,
      nn = 1;
    for (const t of a) rows.push({ t: "del", text: t, o: o++ });
    for (const t of b) rows.push({ t: "add", text: t, n: nn++ });
    return rows;
  }
  // LCS length table (rolling would save memory, but blocks here are small).
  const dp = Array.from({ length: n + 1 }, () => new Int32Array(m + 1));
  for (let i = n - 1; i >= 0; i--)
    for (let j = m - 1; j >= 0; j--)
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  let i = 0,
    j = 0,
    o = 1,
    nn = 1;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      rows.push({ t: "ctx", text: a[i], o: o++, n: nn++ });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      rows.push({ t: "del", text: a[i], o: o++ });
      i++;
    } else {
      rows.push({ t: "add", text: b[j], n: nn++ });
      j++;
    }
  }
  while (i < n) rows.push({ t: "del", text: a[i], o: o++ }), i++;
  while (j < m) rows.push({ t: "add", text: b[j], n: nn++ }), j++;
  return rows;
}
