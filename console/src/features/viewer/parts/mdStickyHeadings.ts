// VS Code-style sticky headings, active only inside .md-scroll.

// setupStickyHeadings pins the heading breadcrumb (# > ## > ###) of the current scroll
// position to the top of the scroller, VS Code-style. An absolutely-positioned overlay
// appended to the scroller is repositioned to the viewport top on scroll and its rows
// rebuilt to the active heading chain. Returns a cleanup that removes it. Heading tops
// are measured live (getBoundingClientRect) so async layout shifts — mermaid diagrams,
// images — self-correct on the next scroll frame.
const MAX_STICKY_HEADS = 4; // keep the nearest N so a deep doc doesn't bury the content

export function setupStickyHeadings(md: HTMLElement, scroller: HTMLElement): () => void {
  const heads = [...md.querySelectorAll<HTMLElement>("h1,h2,h3,h4,h5,h6")].map((h) => ({
    el: h,
    level: Number(h.tagName[1]),
    text: h.textContent || "",
  }));
  if (!heads.length) return () => {};

  const bar = document.createElement("div");
  bar.className = "md-sticky";
  bar.setAttribute("aria-hidden", "true");
  scroller.appendChild(bar);

  let lastSig: string | null = null; // signature of the currently-rendered chain (never a real value)

  // pin keeps the overlay glued to the viewport top; cheap, so it runs on every scroll
  // event (no rAF lag → no jitter). It never touches the DOM rows.
  const pin = () => {
    bar.style.top = scroller.scrollTop + "px";
  };

  // build recomputes the active heading chain and rebuilds the rows ONLY when it changes
  // (rebuilding every frame is what made the bar flicker). Position is handled by pin().
  const build = () => {
    const st = scroller.scrollTop;
    const base = scroller.getBoundingClientRect().top - st; // content-space origin
    // The active chain: for every heading at/above the viewport top, keep a stack where
    // a heading pops any siblings/deeper of >= its level, so the stack is its ancestry.
    const stack: { el: HTMLElement; level: number; text: string }[] = [];
    for (const h of heads) {
      const top = h.el.getBoundingClientRect().top - base;
      if (top > st + 4) break; // headings are in document order → the rest are below
      while (stack.length && stack[stack.length - 1].level >= h.level) stack.pop();
      stack.push(h);
    }
    const shown = stack.length > MAX_STICKY_HEADS ? stack.slice(stack.length - MAX_STICKY_HEADS) : stack;
    const sig = shown.map((h) => h.level + ":" + h.text).join("\n");
    if (sig === lastSig) return; // chain unchanged → leave the DOM alone (no flicker)
    lastSig = sig;
    if (!shown.length) {
      bar.style.display = "none";
      bar.textContent = "";
      return;
    }
    bar.style.display = "";
    bar.textContent = "";
    for (const h of shown) {
      const row = document.createElement("div");
      row.className = "md-sticky-row md-sticky-h" + h.level;
      row.textContent = h.text;
      row.title = h.text;
      row.addEventListener("click", () => h.el.scrollIntoView({ block: "start" }));
      bar.appendChild(row);
    }
  };

  let raf = 0;
  const onScroll = () => {
    pin(); // reposition synchronously — keeps the bar from lagging behind the scroll
    if (raf) return;
    raf = requestAnimationFrame(() => {
      raf = 0;
      build();
    });
  };
  scroller.addEventListener("scroll", onScroll, { passive: true });
  window.addEventListener("resize", onScroll);
  pin();
  build();

  return () => {
    if (raf) cancelAnimationFrame(raf);
    scroller.removeEventListener("scroll", onScroll);
    window.removeEventListener("resize", onScroll);
    bar.remove();
  };
}
