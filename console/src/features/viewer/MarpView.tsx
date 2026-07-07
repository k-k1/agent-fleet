import { useCallback, useEffect, useRef, useState } from "react";

// Custom Marp themes live as CSS files (each with a `/* @theme name */` header) in
// ../marp-themes and are registered with the renderer so decks can select them via
// `theme: <name>` frontmatter (on top of marp-core's built-in default/gaia/uncover).
const THEME_CSS = import.meta.glob<string>("../../marp-themes/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

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
/* Pin the slide to the top of the stage (not vertically centered) so a 16:9 slide
   in a taller viewer keeps its title row at the top; the letterbox falls below. */
.marpit { display: flex; align-items: flex-start; justify-content: center; }
.marpit > svg[data-marpit-svg] {
  width: 100%;
  height: auto;
  max-height: 100%;
  display: block;
  box-shadow: 0 1px 8px rgba(0,0,0,.35);
}
.marpit > section { width: 100%; }
`;

// Marp slides are a fixed 1280x720 frame and do NOT shrink content that overflows
// (a long table just spills past the bottom). fitSection shrinks the slide's font
// until its content fits the frame height — never enlarges, so sparse slides keep
// the theme's intended size. Coordinates are in the frame's CSS px (the SVG scales
// the whole frame to the container), so we compare against a constant 720.
const FRAME_H = 720;
function fitSection(section: HTMLElement | null) {
  if (!section) return;
  section.style.fontSize = ""; // reset any prior fit before measuring
  const base = parseFloat(getComputedStyle(section).fontSize) || 24;
  const h0 = section.scrollHeight;
  if (h0 <= FRAME_H) return;
  // Linear first guess (content height ≈ ∝ font size), then refine: shrinking the
  // font also unwraps lines so height usually drops a bit faster than linear.
  let scale = (FRAME_H / h0) * 0.98;
  section.style.fontSize = base * scale + "px";
  for (let g = 0; g < 8 && section.scrollHeight > FRAME_H; g++) {
    scale *= 0.96;
    section.style.fontSize = base * scale + "px";
  }
}

export function MarpView({ source }: { source?: string }) {
  const hostRef = useRef<HTMLDivElement>(null); // shadow host element
  const stageRef = useRef<HTMLDivElement>(null); // fullscreen target (wraps the host)
  const shadowRef = useRef<ShadowRoot | null>(null);
  const slidesRef = useRef<HTMLElement[]>([]);
  const wheelAtRef = useRef(0); // throttle wheel paging
  const [count, setCount] = useState(0);
  const [cur, setCur] = useState(0);
  const [err, setErr] = useState("");

  const go = useCallback(
    (next: number | ((c: number) => number)) =>
      setCur((c) => Math.max(0, Math.min(next instanceof Function ? next(c) : next, count - 1))),
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
        let out: any;
        try {
          // html:false escapes raw HTML, script:false drops <script>, math:false
          // skips KaTeX — together they keep the injected markup safe and lighter.
          const marp = new Marp({ html: false, script: false, math: false });
          for (const css of Object.values(THEME_CSS)) {
            try {
              marp.themeSet.add(css);
            } catch {
              // a malformed theme file shouldn't break rendering of the deck
            }
          }
          out = marp.render(source ?? "");
        } catch {
          setErr("スライドの描画に失敗しました");
          return;
        }
        if (!alive) return;
        shadow.innerHTML = `<style>${STAGE_CSS}</style><style>${out.css}</style><div class="deck">${out.html}</div>`;
        const slides = [...shadow.querySelectorAll<HTMLElement>(".marpit > svg[data-marpit-svg], .marpit > section")];
        // Auto-fit while every slide is still visible (before the display effect
        // hides the non-current ones, which would zero out their measurements).
        slides.forEach((el) => fitSection((el.querySelector("section") as HTMLElement) || el));
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
    function onKey(e: KeyboardEvent) {
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

  // Mouse-wheel paging: down → next, up → prev. Throttled so one flick doesn't
  // skip many slides. Non-passive so we can stop the page from scrolling. A tall
  // slide that overflows the stage still scrolls first; we only page at its edges.
  useEffect(() => {
    const stage = stageRef.current;
    if (!stage) return;
    const onWheel = (e: WheelEvent) => {
      const dy = e.deltaY;
      if (!dy) return;
      const atTop = stage.scrollTop <= 0;
      const atBottom = stage.scrollTop + stage.clientHeight >= stage.scrollHeight - 1;
      if ((dy > 0 && !atBottom) || (dy < 0 && !atTop)) return; // let it scroll first
      e.preventDefault();
      const now = e.timeStamp || performance.now();
      if (now - wheelAtRef.current < 280) return;
      wheelAtRef.current = now;
      go((c) => c + (dy > 0 ? 1 : -1));
    }
    stage.addEventListener("wheel", onWheel, { passive: false });
    return () => stage.removeEventListener("wheel", onWheel);
  }, [go]);

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
        {err ? (
          <div className="filebody muted">({err})</div>
        ) : (
          <>
            {/* Left / right click zones page back / forward; the cursor turns into a
                ←/→ arrow over them (see styles.css). The middle is inert. */}
            <div className="marp-nav left" onClick={() => go((c) => c - 1)} title="前のスライド" />
            <div className="marp-host" ref={hostRef} />
            <div className="marp-nav right" onClick={() => go((c) => c + 1)} title="次のスライド" />
          </>
        )}
      </div>
    </div>
  );
}
