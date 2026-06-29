import { useRef } from "react";
import { useApp } from "../state.jsx";
import Pane from "./Pane.jsx";

// PaneHost lays the main-area panes out as a CSS grid: one column when single, two
// columns split by a draggable divider when vertical. The divider updates the split
// ratio live; each pane's terminal refits via its own ResizeObserver (see term.js).
export default function PaneHost() {
  const { layout, activePaneId, setActivePane, splitPane, closePane, setRatio } = useApp();
  const hostRef = useRef(null);
  const dragging = useRef(false);

  // Drag the divider: translate the pointer x into a left-pane fraction of the host
  // width. Listen on window so the drag keeps tracking past the thin handle.
  const onDividerDown = (e) => {
    e.preventDefault();
    dragging.current = true;
    document.body.classList.add("col-resizing");
    const onMove = (ev) => {
      if (!dragging.current || !hostRef.current) return;
      const r = hostRef.current.getBoundingClientRect();
      setRatio((ev.clientX - r.left) / r.width);
    };
    const onUp = () => {
      dragging.current = false;
      document.body.classList.remove("col-resizing");
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  };

  const single = layout.split === "single";
  const cols = single ? "1fr" : `${layout.ratio}fr 6px ${1 - layout.ratio}fr`;

  const paneEl = (pane) => (
    <Pane
      key={pane.id}
      pane={pane}
      active={single || pane.id === activePaneId}
      split={layout.split}
      onActivate={setActivePane}
      onSplit={splitPane}
      onClose={closePane}
    />
  );

  return (
    <div className="panehost" ref={hostRef} style={{ gridTemplateColumns: cols }}>
      {single
        ? paneEl(layout.panes[0])
        : [
            paneEl(layout.panes[0]),
            <div key="divider" className="pane-divider" onPointerDown={onDividerDown} />,
            paneEl(layout.panes[1]),
          ]}
    </div>
  );
}
