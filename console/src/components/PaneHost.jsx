import { useEffect, useMemo, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import { paneOrdinals } from "../lib/panebadge.js";
import Pane from "./Pane.jsx";

const D = 6; // divider thickness in px

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

// PaneHost lays the main area out as up to 4 columns, each a stack of 1 or 2 panes.
// All panes (and the dividers) are FLAT, position:absolute children of one stable
// .panehost — a pane's column/row is expressed purely by its computed CSS rect, not
// by DOM nesting. So dragging a pane to another column only changes its rect: React
// keeps the same keyed DOM node (no remount), and term.js never re-opens / re-parents
// the xterm (which left the moved terminal blank). Moves become a pure resize.
export default function PaneHost() {
  const { layout, activePaneId, setActivePane, splitRight, splitDown, closePane, setColRatios, setRowRatio, swapPanes, dropSplit, sessions } =
    useApp();
  const hostRef = useRef(null);
  const isMobile = useIsMobile();

  // Look up a pane's session so the terminal header can render it like the left-pane
  // Sessions row (kind badge + name + state). Memoized so a layout-only re-render
  // (drag/move) doesn't rebuild the Map.
  const sessionByName = useMemo(() => new Map((sessions || []).map((s) => [s.name, s])), [sessions]);

  const cols = layout.cols;
  const N = cols.length;
  const ratios = layout.colRatios;
  const total = cols.reduce((n, c) => n + c.panes.length, 0);

  // Pane id → visual-order ordinal (1-based), the badge shared with the Sessions
  // list and the layout mini-map. Only meaningful when split (total > 1).
  const ordinalById = useMemo(() => paneOrdinals(layout), [layout]);

  // Cumulative left-ratio before column i (sum of widths to its left).
  const cum = [];
  for (let i = 0, acc = 0; i < N; i++) {
    cum.push(acc);
    acc += ratios[i] ?? 0;
  }
  // Width budget = 100% minus the (N-1) column dividers; height budget = 100% minus
  // the one row divider in a split column. Positions are calc() over these so the fr
  // ratios and the fixed-px dividers compose exactly.
  const Wb = `(100% - ${(N - 1) * D}px)`;
  const Hb = `(100% - ${D}px)`;
  const colLeft = (i) => `calc(${cum[i]} * ${Wb} + ${i * D}px)`;
  const colWidth = (i) => `calc(${ratios[i]} * ${Wb})`;

  // Drag a column divider at index i: shift width fraction between col i and i+1.
  const onColDown = (i) => (e) => {
    e.preventDefault();
    const start = e.clientX;
    const base = ratios.slice();
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

  // Drag a row divider inside a column: columns span the full host height now, so the
  // top row's fraction is just the pointer's y within the host.
  const onRowDown = (colId) => (e) => {
    e.preventDefault();
    const rect = hostRef.current.getBoundingClientRect();
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

  // A lone, empty terminal is the base state — nothing to close. Any other single
  // pane (a session / file / SCM, or a terminal showing a session) CAN be closed:
  // closePane resets it to a blank terminal (clears its content). Mirrors the WsBar
  // "全ペインを閉じる" enablement.
  const isBlankSingle = (pane) =>
    total === 1 && pane.kind === "terminal" && !pane.session && !pane.filePath && !pane.scmRepo;

  const renderPane = (pane, col, i, rect) => (
    <Pane
      key={pane.id}
      pane={pane}
      style={{ position: "absolute", ...rect }}
      active={total > 1 && pane.id === activePaneId}
      single={total === 1}
      // Desktop: up to 4 columns, each splittable top/bottom. Mobile: no extra
      // columns, only a single top/bottom split (max 2 panes total).
      canSplitRight={!isMobile && N < 4}
      canSplitDown={isMobile ? total < 2 : col.panes.length < 2}
      canClose={total > 1 || !isBlankSingle(pane)}
      canDrag={total > 1}
      onActivate={setActivePane}
      onSplitRight={splitRight}
      onSplitDown={splitDown}
      onClose={closePane}
      onSwap={swapPanes}
      onDropSplit={dropSplit}
      sessionMeta={pane.session ? sessionByName.get(pane.session) : null}
      ordinal={total > 1 ? ordinalById.get(pane.id) : null}
    />
  );

  const paneEls = [];
  const dividerEls = [];
  cols.forEach((col, i) => {
    const panes = col.panes;
    // On a phone only the first column is shown full-width (matching the old layout);
    // panes in any other column stay mounted (terminal + socket alive) but hidden.
    if (isMobile && i > 0) {
      panes.forEach((p) => paneEls.push(renderPane(p, col, i, { display: "none" })));
      return;
    }
    const left = isMobile ? "0" : colLeft(i);
    const width = isMobile ? "100%" : colWidth(i);
    if (panes.length === 1) {
      paneEls.push(renderPane(panes[0], col, i, { left, top: 0, width, height: "100%" }));
    } else {
      const r = col.rowRatio;
      paneEls.push(renderPane(panes[0], col, i, { left, top: 0, width, height: `calc(${r} * ${Hb})` }));
      dividerEls.push(
        <div
          key={`r${col.id}`}
          className="pane-divider row"
          style={{ left, width, top: `calc(${r} * ${Hb})`, height: `${D}px` }}
          onPointerDown={onRowDown(col.id)}
        />,
      );
      paneEls.push(renderPane(panes[1], col, i, { left, top: `calc(${r} * ${Hb} + ${D}px)`, width, height: `calc(${1 - r} * ${Hb})` }));
    }
    if (!isMobile && i < N - 1) {
      dividerEls.push(
        <div
          key={`c${col.id}`}
          className="pane-divider col"
          style={{ left: `calc(${cum[i + 1]} * ${Wb} + ${i * D}px)`, top: 0, width: `${D}px`, height: "100%" }}
          onPointerDown={onColDown(i)}
        />,
      );
    }
  });

  return (
    <div className="panehost" ref={hostRef}>
      {paneEls}
      {dividerEls}
    </div>
  );
}
