import { useEffect, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import Pane from "./Pane.jsx";

// Tracks the mobile breakpoint (matches the 760px media query in styles.css). On a
// phone the split is limited to a single column split top/bottom (max 2 panes).
function useIsMobile() {
  const [m, setM] = useState(() => typeof window !== "undefined" && window.matchMedia("(max-width: 760px)").matches);
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 760px)");
    const fn = () => setM(mq.matches);
    mq.addEventListener("change", fn);
    return () => mq.removeEventListener("change", fn);
  }, []);
  return m;
}

// PaneHost lays the main area out as up to 4 columns (a CSS grid), each column a
// sub-grid of 1 or 2 rows. Column dividers resize widths; a row divider inside a
// split column resizes its top/bottom heights. Every pane's terminal refits via its
// own ResizeObserver (see term.js), so dragging either divider reflows the grids.
export default function PaneHost() {
  const { layout, activePaneId, setActivePane, splitRight, splitDown, closePane, setColRatios, setRowRatio, swapPanes, dropSplit } =
    useApp();
  const hostRef = useRef(null);
  const isMobile = useIsMobile();

  const cols = layout.cols;
  const colCount = cols.length;
  const total = cols.reduce((n, c) => n + c.panes.length, 0);

  // Drag a column divider at index i (between col i and i+1): shift width fraction
  // from one neighbor to the other based on pointer x within the host.
  const onColDown = (i) => (e) => {
    e.preventDefault();
    const start = e.clientX;
    const base = layout.colRatios.slice();
    const rect = hostRef.current.getBoundingClientRect();
    document.body.classList.add("col-resizing");
    const onMove = (ev) => {
      const d = (ev.clientX - start) / rect.width;
      const next = base.slice();
      next[i] = Math.max(0.1, base[i] + d);
      next[i + 1] = Math.max(0.1, base[i + 1] - d);
      setColRatios(next);
    };
    const onUp = () => {
      document.body.classList.remove("col-resizing");
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  };

  // Drag a row divider inside column colId: translate pointer y into the top row's
  // height fraction of that column's element.
  const onRowDown = (colId) => (e) => {
    e.preventDefault();
    const colEl = e.currentTarget.parentElement; // the .panecol
    const rect = colEl.getBoundingClientRect();
    document.body.classList.add("row-resizing");
    const onMove = (ev) => setRowRatio(colId, (ev.clientY - rect.top) / rect.height);
    const onUp = () => {
      document.body.classList.remove("row-resizing");
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  };

  // grid-template-columns: r0fr 6px r1fr 6px … (a 6px track per divider).
  const colsTemplate = layout.colRatios.map((r) => `${r}fr`).join(" 6px ");

  const renderPane = (pane, col) => (
    <Pane
      key={pane.id}
      pane={pane}
      active={total > 1 && pane.id === activePaneId}
      single={total === 1}
      // Desktop: up to 4 columns, each splittable top/bottom. Mobile: no extra
      // columns, only a single top/bottom split (max 2 panes total).
      canSplitRight={!isMobile && colCount < 4}
      canSplitDown={isMobile ? total < 2 : col.panes.length < 2}
      canClose={total > 1}
      canDrag={total > 1}
      onActivate={setActivePane}
      onSplitRight={splitRight}
      onSplitDown={splitDown}
      onClose={closePane}
      onSwap={swapPanes}
      onDropSplit={dropSplit}
    />
  );

  return (
    <div className="panehost" ref={hostRef} style={{ gridTemplateColumns: colsTemplate }}>
      {cols.map((col, i) => [
        <div
          key={col.id}
          className="panecol"
          style={{
            gridTemplateRows:
              col.panes.length === 2 ? `${col.rowRatio}fr 6px ${1 - col.rowRatio}fr` : "minmax(0, 1fr)",
          }}
        >
          {renderPane(col.panes[0], col)}
          {col.panes.length === 2 && (
            <div className="pane-divider row" onPointerDown={onRowDown(col.id)} />
          )}
          {col.panes.length === 2 && renderPane(col.panes[1], col)}
        </div>,
        i < colCount - 1 && (
          <div key={`d${col.id}`} className="pane-divider col" onPointerDown={onColDown(i)} />
        ),
      ])}
    </div>
  );
}
