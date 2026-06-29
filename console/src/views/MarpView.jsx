import { useCallback, useEffect, useRef, useState } from "react";

// MarpView renders a `marp: true` Markdown document as a real Marp slide deck and
// presents it one slide at a time (stepper + fullscreen + keyboard). marp-core is
// loaded lazily — only when a deck is actually opened — to keep it out of the main
// bundle (mirrors how MarkdownView lazy-loads mermaid).
//
// The rendered HTML + theme CSS are injected into a Shadow DOM so Marp's theme
// (which styles bare `section`, `h1`, … globally) cannot leak into the Console and
// the Console's own styles cannot bleed into the slides.
//
// Marp (inline-SVG mode) emits one `<svg data-marpit-svg viewBox="0 0 1280 720">`
// per slide under `<div class="marpit">`; we toggle their display to step.

// Shadow-local CSS: size each slide to the stage, preserving the 16:9 ratio. Inline
// SVG with a viewBox + width:100% lets the browser derive height from the ratio.
const STAGE_CSS = `
:host { display: block; height: 100%; }
.deck, .marpit { width: 100%; height: 100%; margin: 0; }
.marpit { display: flex; align-items: center; justify-content: center; }
.marpit > svg[data-marpit-svg] {
  width: 100%;
  height: auto;
  max-height: 100%;
  display: block;
  box-shadow: 0 1px 8px rgba(0,0,0,.35);
}
.marpit > section { width: 100%; }
`;

export default function MarpView({ source }) {
  const hostRef = useRef(null); // shadow host element
  const stageRef = useRef(null); // fullscreen target (wraps the host)
  const shadowRef = useRef(null);
  const slidesRef = useRef([]);
  const [count, setCount] = useState(0);
  const [cur, setCur] = useState(0);
  const [err, setErr] = useState("");

  const go = useCallback(
    (next) => setCur((c) => Math.max(0, Math.min(next instanceof Function ? next(c) : next, count - 1))),
    [count],
  );

  // Render the deck into the Shadow DOM whenever the source changes.
  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    let alive = true;
    if (!shadowRef.current) shadowRef.current = host.attachShadow({ mode: "open" });
    const shadow = shadowRef.current;
    setErr("");
    setCur(0);

    import("@marp-team/marp-core")
      .then(({ Marp }) => {
        if (!alive) return;
        let out;
        try {
          // html:false escapes raw HTML, script:false drops <script>, math:false
          // skips KaTeX — together they keep the injected markup safe and lighter.
          const marp = new Marp({ html: false, script: false, math: false });
          out = marp.render(source ?? "");
        } catch {
          setErr("スライドの描画に失敗しました");
          return;
        }
        if (!alive) return;
        shadow.innerHTML = `<style>${STAGE_CSS}</style><style>${out.css}</style><div class="deck">${out.html}</div>`;
        const slides = [...shadow.querySelectorAll(".marpit > svg[data-marpit-svg], .marpit > section")];
        slidesRef.current = slides;
        setCount(slides.length);
      })
      .catch(() => alive && setErr("Marp の読み込みに失敗しました"));

    return () => {
      alive = false;
    };
  }, [source]);

  // Show only the current slide.
  useEffect(() => {
    slidesRef.current.forEach((s, i) => {
      s.style.display = i === cur ? "" : "none";
    });
  }, [cur, count]);

  // Keyboard navigation while the stage has focus (arrows / space / pgup-dn / home-end).
  useEffect(() => {
    const stage = stageRef.current;
    if (!stage) return;
    function onKey(e) {
      switch (e.key) {
        case "ArrowRight":
        case "PageDown":
        case " ":
          e.preventDefault();
          go((c) => c + 1);
          break;
        case "ArrowLeft":
        case "PageUp":
          e.preventDefault();
          go((c) => c - 1);
          break;
        case "Home":
          e.preventDefault();
          go(0);
          break;
        case "End":
          e.preventDefault();
          go(count - 1);
          break;
        default:
      }
    }
    stage.addEventListener("keydown", onKey);
    return () => stage.removeEventListener("keydown", onKey);
  }, [go, count]);

  const toggleFs = useCallback(() => {
    const stage = stageRef.current;
    if (!stage) return;
    if (!document.fullscreenElement) stage.requestFullscreen?.();
    else document.exitFullscreen?.();
  }, []);

  return (
    <div className="marp-wrap">
      <div className="marp-toolbar">
        <button type="button" className="seg-btn" onClick={() => go((c) => c - 1)} disabled={cur <= 0} title="前のスライド (←)">
          ◀
        </button>
        <span className="marp-counter mono">
          {count ? cur + 1 : 0} / {count}
        </span>
        <button
          type="button"
          className="seg-btn"
          onClick={() => go((c) => c + 1)}
          disabled={count === 0 || cur >= count - 1}
          title="次のスライド (→)"
        >
          ▶
        </button>
        <button type="button" className="seg-btn marp-fs" onClick={toggleFs} title="全画面">
          ⤢
        </button>
      </div>
      <div className="marp-stage" ref={stageRef} tabIndex={0}>
        {err ? <div className="filebody muted">({err})</div> : <div className="marp-host" ref={hostRef} />}
      </div>
    </div>
  );
}
