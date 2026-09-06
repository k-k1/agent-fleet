// Ctrl+R history search in the mirror composer. The pure matching is covered in
// historySearch.test.ts; what is pinned here is the part that can only break in the browser: which
// keys are taken (and, just as important, which are handed back to the browser), what the composer
// shows while stepping, and that cancelling gives the user their own text back.
import { describe, it, expect, afterEach } from "vitest";
import { useRef, useState } from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useHistorySearch } from "./parts/useHistorySearch.ts";
import { HistorySearchBar } from "./parts/HistorySearchBar.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

// Oldest first, as MirrorView builds it.
const HIST = ["npm test を流して", "ビルドを直して", "テストを追加", "npm run build"];

let histIdx: number | null = null;

/** The composer, reduced to what the search touches: a textarea, the draft, and the bar. */
function Harness({ history = HIST, locked = false, initial = "" }: { history?: string[]; locked?: boolean; initial?: string }) {
  const [draft, setDraft] = useState(initial);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const hs = useHistorySearch({ history, draft, setDraft, setHistIdx: (v) => (histIdx = v), inputRef, composerLocked: locked });
  return (
    <div>
      {hs.open && (
        <HistorySearchBar
          inputRef={hs.queryRef}
          query={hs.query}
          count={hs.count}
          pos={hs.pos}
          failed={hs.failed}
          onQuery={hs.onQuery}
          onKeyDown={hs.onQueryKeyDown}
          onBlur={hs.onQueryBlur}
          onCancel={hs.cancel}
        />
      )}
      <textarea ref={inputRef} data-testid="composer" value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={hs.handleKeyDown} />
    </div>
  );
}

function mount(props: { history?: string[]; locked?: boolean; initial?: string } = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<Harness {...props} />));
}

const composer = () => host!.querySelector<HTMLTextAreaElement>('[data-testid="composer"]')!;
const bar = () => host!.querySelector<HTMLDivElement>(".mirror-histsearch");
const queryField = () => host!.querySelector<HTMLInputElement>(".mirror-histsearch-input");
const count = () => host!.querySelector<HTMLSpanElement>(".mirror-histsearch-count")?.textContent;

/** Returns the event so the caller can assert whether the browser's own action survived. */
function key(el: HTMLElement, init: KeyboardEventInit & { key: string }): KeyboardEvent {
  const e = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
  act(() => {
    el.dispatchEvent(e);
  });
  return e;
}

/** Type into the query field the way a user does (React reads value off the element). */
function type(text: string) {
  const el = queryField()!;
  act(() => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    setter.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  histIdx = null;
});

describe("Ctrl+R history search", () => {
  it("opens on Ctrl+R and swallows the browser's reload", () => {
    mount();
    const e = key(composer(), { key: "r", ctrlKey: true });
    expect(e.defaultPrevented).toBe(true);
    expect(bar()).not.toBeNull();
    expect(count()).toBe("4"); // nothing previewed yet: how far back it can go
    expect(composer().value).toBe(""); // the draft is untouched until a match is chosen
  });

  it("leaves reload alone when there is nothing to search, and while the composer is locked", () => {
    mount({ history: [] });
    const e = key(composer(), { key: "r", ctrlKey: true });
    expect(e.defaultPrevented).toBe(false);
    expect(bar()).toBeNull();
    act(() => root!.render(<Harness locked />));
    expect(key(composer(), { key: "r", ctrlKey: true }).defaultPrevented).toBe(false);
  });

  it("ignores ⌘R (macOS reload) and a Ctrl+R that arrives mid-IME-composition", () => {
    mount();
    expect(key(composer(), { key: "r", metaKey: true, ctrlKey: true }).defaultPrevented).toBe(false);
    expect(key(composer(), { key: "r", ctrlKey: true, isComposing: true }).defaultPrevented).toBe(false);
    expect(bar()).toBeNull();
  });

  it("previews the newest match in the composer as the query is typed", () => {
    mount();
    key(composer(), { key: "r", ctrlKey: true });
    type("npm");
    expect(composer().value).toBe("npm run build");
    expect(count()).toBe("1/2");
  });

  it("walks older with Ctrl+R and back with Ctrl+S, clamped at both ends", () => {
    mount();
    key(composer(), { key: "r", ctrlKey: true });
    type("npm");
    key(queryField()!, { key: "r", ctrlKey: true });
    expect(composer().value).toBe("npm test を流して");
    expect(count()).toBe("2/2");
    key(queryField()!, { key: "r", ctrlKey: true }); // no older match: stay put
    expect(composer().value).toBe("npm test を流して");
    key(queryField()!, { key: "s", ctrlKey: true });
    expect(composer().value).toBe("npm run build");
    expect(count()).toBe("1/2");
  });

  it("keeps the last match on screen when the query stops matching, and says so", () => {
    mount();
    key(composer(), { key: "r", ctrlKey: true });
    type("npm");
    type("npmx");
    expect(composer().value).toBe("npm run build"); // bash leaves the line as it was
    expect(bar()!.className).toContain("failed");
    expect(count()).toBe("0");
  });

  it("accepts into the composer on Enter without sending, and hands ↑ recall the right position", () => {
    mount();
    key(composer(), { key: "r", ctrlKey: true });
    type("ビルド");
    key(queryField()!, { key: "Enter" });
    expect(bar()).toBeNull();
    expect(composer().value).toBe("ビルドを直して");
    expect(histIdx).toBe(1); // the entry's index in history, so ↑ continues from there
  });

  it("restores what the user was typing on Esc", () => {
    mount({ initial: "書きかけの文" });
    key(composer(), { key: "r", ctrlKey: true });
    type("テスト");
    expect(composer().value).toBe("テストを追加");
    key(queryField()!, { key: "Escape" });
    expect(bar()).toBeNull();
    expect(composer().value).toBe("書きかけの文");
    expect(histIdx).toBeNull();
  });

  it("leaves ↑↓ and Enter to the IME while a candidate window is open", () => {
    mount();
    key(composer(), { key: "r", ctrlKey: true });
    type("npm");
    key(queryField()!, { key: "ArrowUp", isComposing: true });
    expect(composer().value).toBe("npm run build"); // no step
    key(queryField()!, { key: "Enter", isComposing: true });
    expect(bar()).not.toBeNull(); // the search is still open: that Enter settled the candidate
  });
});
