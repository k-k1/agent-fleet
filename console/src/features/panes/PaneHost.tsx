// PaneHost — lays the main area out as up to 4 columns, each a stack of 1 or 2
// panes. All panes (and dividers) are FLAT, position:absolute children of one
// stable .panehost: a pane's column/row is expressed purely by its computed CSS
// rect, not DOM nesting. In the split profile the pane id keys that DOM node,
// so moving a pane keeps its xterm with it. In the tabbed profile a visual cell
// keeps a geometric key instead: selecting a tab changes the selected view id,
// but must not remount the cell and reset its tab-strip scroll position.
import { memo, useMemo, useRef } from "react";
import type { CSSProperties, PointerEvent as RPointerEvent, ReactNode } from "react";
import { useLayoutStore } from "../../layout/store.ts";
import { paneOrdinals } from "../../layout/badges.ts";
import { MAX_TAB_COLS, selectedView } from "../../layout/ops.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useIsMobile } from "../../lib/device.ts";
import { Pane } from "./Pane.tsx";
import type { Cell, Column } from "../../layout/types.ts";

const D = 6; // divider thickness in px

export const PaneHost = memo(function PaneHost() {
  const layout = useLayoutStore((s) => s.layout);
  const setActive = useLayoutStore((s) => s.setActive);
  const closePane = useLayoutStore((s) => s.closePane);
  const setColRatios = useLayoutStore((s) => s.setColRatios);
  const setRowRatio = useLayoutStore((s) => s.setRowRatio);
  const swapPanes = useLayoutStore((s) => s.swapPanes);
  const dropSplit = useLayoutStore((s) => s.dropSplit);
  const sessions = useSessionsStore((s) => s.sessions);
  const hostRef = useRef<HTMLDivElement>(null);
  const isMobile = useIsMobile();

  // Session lookup for the terminal headers; memoized so layout-only re-renders
  // (drag/move) don't rebuild the Map.
  const sessionByName = useMemo(() => new Map(sessions.map((s) => [s.name, s] as const)), [sessions]);

  const cols = layout.cols;
  const N = cols.length;
  const ratios = layout.colRatios;
  const total = cols.reduce((n, c) => n + c.cells.length, 0);
  const ordinalById = useMemo(() => paneOrdinals(layout), [layout]);

  // Cumulative left-ratio before column i.
  const cum: number[] = [];
  for (let i = 0, acc = 0; i < N; i++) {
    cum.push(acc);
    acc += ratios[i] ?? 0;
  }
  // Width budget = 100% minus the (N-1) column dividers; height budget = 100%
  // minus the one row divider in a split column.
  const Wb = `(100% - ${(N - 1) * D}px)`;
  const Hb = `(100% - ${D}px)`;
  const colLeft = (i: number) => `calc(${cum[i]} * ${Wb} + ${i * D}px)`;
  const colWidth = (i: number) => `calc(${ratios[i]} * ${Wb})`;

  const onColDown = (i: number) => (e: RPointerEvent) => {
    e.preventDefault();
    const start = e.clientX;
    const base = ratios.slice();
    const rect = hostRef.current?.getBoundingClientRect();
    if (!rect) return;
    document.body.classList.add("col-resizing");
    const onMove = (ev: PointerEvent) => {
      // ドラッグ量そのものを「両隣が 0.1 を割らない範囲」にクランプする。片側だけ
      // Math.max(0.1, …) で底打ちすると合計が 1 を超え、右端列がはみ出す比率が
      // そのまま永続化されてしまう（setColRatios / migrate 側の正規化は保険）。
      const d = (ev.clientX - start) / rect.width;
      const dd = Math.min(Math.max(d, 0.1 - base[i]), base[i + 1] - 0.1);
      const next = base.slice();
      next[i] = base[i] + dd;
      next[i + 1] = base[i + 1] - dd;
      setColRatios(next);
    };
    const onUp = () => {
      document.body.classList.remove("col-resizing");
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    // タッチ中断（スクロール横取り等）は pointerup が来ない — リスナと body クラスが
    // 残留してドラッグ状態が固着しないよう pointercancel でも解除する。
    window.addEventListener("pointercancel", onUp);
  };

  const onRowDown = (colId: string) => (e: RPointerEvent) => {
    e.preventDefault();
    const rect = hostRef.current?.getBoundingClientRect();
    if (!rect) return;
    document.body.classList.add("row-resizing");
    const onMove = (ev: PointerEvent) => setRowRatio(colId, (ev.clientY - rect.top) / rect.height);
    const onUp = () => {
      document.body.classList.remove("row-resizing");
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onUp); // 同上: タッチ中断でも解除
  };

  // A lone, empty terminal is the base state — nothing to close.
  const isBlankSingle = (cell: Cell) => total === 1 && cell.views.length === 0;

  const renderPane = (cell: Cell, col: Column, row: number, rect: CSSProperties) => {
    const pane = selectedView(cell);
    return (
    <Pane
      key={cell.id}
      cell={cell}
      pane={pane}
      style={{ position: "absolute", ...rect }}
      active={cell.id === layout.activeCellId}
      single={total === 1}
      tabbed={layout.mode === "tabs"}
      // Desktop: up to 4 columns, each splittable top/bottom. Mobile: only a
      // single top/bottom split (max 2 panes total).
      canSplitRight={!isMobile && N < (layout.mode === "tabs" ? MAX_TAB_COLS : 4)}
      canSplitDown={isMobile ? total < 2 : col.cells.length < 2}
      canClose={total > 1 || !isBlankSingle(cell)}
      canDrag={total > 1}
      onActivate={setActive}
      onClose={closePane}
      onSwap={swapPanes}
      onDropSplit={dropSplit}
      sessionMeta={pane?.session ? sessionByName.get(pane.session) : null}
      ordinal={total > 1 ? ordinalById.get(cell.id) : null}
    />
    );
  };

  const paneEls: ReactNode[] = [];
  const dividerEls: ReactNode[] = [];
  cols.forEach((col, i) => {
    const panes = col.cells;
    // On a phone only the first column shows; panes in other columns stay
    // mounted (terminal + socket alive) but hidden.
    if (isMobile && i > 0) {
      panes.forEach((p, row) => paneEls.push(renderPane(p, col, row, { display: "none" })));
      return;
    }
    const left = isMobile ? "0" : colLeft(i);
    const width = isMobile ? "100%" : colWidth(i);
    if (panes.length === 1) {
      paneEls.push(renderPane(panes[0], col, 0, { left, top: 0, width, height: "100%" }));
    } else {
      const r = col.rowRatio;
      paneEls.push(renderPane(panes[0], col, 0, { left, top: 0, width, height: `calc(${r} * ${Hb})` }));
      dividerEls.push(
        <div
          key={`r${col.id}`}
          className="pane-divider row"
          style={{ left, width, top: `calc(${r} * ${Hb})`, height: `${D}px` }}
          onPointerDown={onRowDown(col.id)}
        />,
      );
      paneEls.push(
        renderPane(panes[1], col, 1, {
          left,
          top: `calc(${r} * ${Hb} + ${D}px)`,
          width,
          height: `calc(${1 - r} * ${Hb})`,
        }),
      );
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
});
