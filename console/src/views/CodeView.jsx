import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

// CodeView renders highlighted code with an optional line-number gutter and a
// VSCode-style minimap on the right edge: a scaled-down mirror of the file with a
// draggable viewport box. Clicking/dragging the minimap scrolls the code.
const SCALE = 0.16; // minimap size relative to the real code

export default function CodeView({ html, lines, lineNumbers, wrap, minimap }) {
  const scrollRef = useRef(null);
  const miniInnerRef = useRef(null);
  const [vp, setVp] = useState({ visible: false, top: 0, height: 0, offset: 0 });

  const sync = useCallback(() => {
    const sc = scrollRef.current;
    const inner = miniInnerRef.current;
    if (!sc || !inner) return;
    const contentH = sc.scrollHeight;
    const paneViewH = sc.clientHeight;
    const miniPaneH = sc.clientHeight; // minimap pane spans the code area height
    if (contentH <= paneViewH + 1) {
      setVp((v) => (v.visible ? { ...v, visible: false } : v));
      return;
    }
    const visualH = inner.offsetHeight * SCALE; // scaled mirror height
    const ratio = visualH / contentH;
    const vpH = paneViewH * ratio;
    let offset = 0;
    if (visualH > miniPaneH) {
      offset = (sc.scrollTop / (contentH - paneViewH)) * (visualH - miniPaneH);
    }
    setVp({ visible: true, top: sc.scrollTop * ratio - offset, height: vpH, offset });
  }, []);

  useLayoutEffect(() => {
    sync();
  }, [html, lineNumbers, wrap, minimap, sync]);

  useEffect(() => {
    const sc = scrollRef.current;
    if (!sc) return;
    const h = () => sync();
    sc.addEventListener("scroll", h, { passive: true });
    window.addEventListener("resize", h);
    return () => {
      sc.removeEventListener("scroll", h);
      window.removeEventListener("resize", h);
    };
  }, [sync]);

  // Map a Y within the minimap pane to a scroll position (centred on the cursor).
  const scrollToMini = (clientY, paneEl) => {
    const sc = scrollRef.current;
    const inner = miniInnerRef.current;
    if (!sc || !inner) return;
    const rect = paneEl.getBoundingClientRect();
    const yInPane = clientY - rect.top;
    const visualPos = yInPane + vp.offset; // position within the scaled mirror
    const main = visualPos / SCALE; // back to real code coordinates
    sc.scrollTop = main - sc.clientHeight / 2;
  };

  const onMiniDown = (e) => {
    const pane = e.currentTarget;
    scrollToMini(e.clientY, pane);
    const move = (ev) => scrollToMini(ev.clientY, pane);
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  };

  // Touch: tap or swipe up/down on the minimap to scroll. `touch-action: none` on
  // .minimap (styles.css) hands us the gesture so the page doesn't scroll instead.
  const onMiniTouch = (e) => {
    const t = e.touches[0];
    if (t) scrollToMini(t.clientY, e.currentTarget);
  };

  return (
    <div className="codeview-wrap">
      <div className={"codeview" + (wrap ? " wrap" : "")} ref={scrollRef}>
        {lineNumbers && (
          <pre className="gutter" aria-hidden="true">
            {Array.from({ length: Math.max(lines, 1) }, (_, i) => i + 1).join("\n")}
          </pre>
        )}
        <pre className="code">
          <code className="hljs" dangerouslySetInnerHTML={{ __html: html }} />
        </pre>
      </div>
      {minimap && (
        <div
          className="minimap"
          onMouseDown={onMiniDown}
          onTouchStart={onMiniTouch}
          onTouchMove={onMiniTouch}
        >
          <div className="minimap-inner" ref={miniInnerRef} style={{ transform: `translateY(${-vp.offset}px) scale(${SCALE})` }}>
            <pre className="code">
              <code className="hljs" dangerouslySetInnerHTML={{ __html: html }} />
            </pre>
          </div>
          {vp.visible && <div className="minimap-viewport" style={{ top: vp.top, height: vp.height }} />}
        </div>
      )}
    </div>
  );
}
