import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { IconButton } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";

const MATCH_HIGHLIGHT = "pane-find-match";
const CURRENT_HIGHLIGHT = "pane-find-current";

// CSS Custom Highlights are now available in the browsers supported by the Console,
// but are not included in every TypeScript DOM library yet. Keep the small surface we
// use local instead of widening globals for the whole application.
interface HighlightRegistry {
  set(name: string, highlight: unknown): void;
  delete(name: string): void;
}
interface HighlightConstructor {
  new (...ranges: Range[]): unknown;
}
interface HighlightGlobals {
  CSS?: typeof CSS & { highlights?: HighlightRegistry };
  Highlight?: HighlightConstructor;
}

let highlightOwner: symbol | null = null;

const ignoredSelector = [
  ".pane-find",
  ".pane-controls",
  ".pane-grip",
  ".view-head",
  ".minimap",
  ".cm-gutters", // CodeMirror line-number gutter: a numeric search must not hit line numbers
  ".cl-gutter",
  ".dv-gutter",
  ".dl-num",
  "[data-pane-find-ignore]",
  "[hidden]",
  "[aria-hidden='true']",
  "script",
  "style",
  "noscript",
  "input",
  "textarea",
  "select",
].join(",");

function highlightAPI(): { registry: HighlightRegistry; Highlight: HighlightConstructor } | null {
  const globals = globalThis as typeof globalThis & HighlightGlobals;
  const registry = globals.CSS?.highlights;
  const Highlight = globals.Highlight;
  return registry && Highlight ? { registry, Highlight } : null;
}

export function matchOffsets(text: string, query: string): Array<{ start: number; end: number }> {
  if (!query) return [];
  const escaped = query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(escaped, "giu");
  const result: Array<{ start: number; end: number }> = [];
  for (const match of text.matchAll(re)) {
    const start = match.index;
    const end = start + match[0].length;
    result.push({ start, end });
    // RegExp#matchAll advances by one code point for an empty match. Queries are
    // non-empty here, but retain the guard in case normalization changes later.
    if (end === start) break;
  }
  return result;
}

interface TextPart {
  node: Text;
  start: number;
  end: number;
}

function findRanges(root: HTMLElement, query: string): Range[] {
  const parts: TextPart[] = [];
  let text = "";
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      const parent = node.parentElement;
      if (!parent || !node.nodeValue || parent.closest(ignoredSelector)) return NodeFilter.FILTER_REJECT;
      // closest() covers semantic hiding; this catches CSS-only hiding (closed modes,
      // responsive controls, etc.) without forcing geometry for every ancestor.
      if (parent.getClientRects().length === 0) return NodeFilter.FILTER_REJECT;
      return NodeFilter.FILTER_ACCEPT;
    },
  });
  for (let node = walker.nextNode() as Text | null; node; node = walker.nextNode() as Text | null) {
    const value = node.nodeValue || "";
    const start = text.length;
    text += value;
    parts.push({ node, start, end: start + value.length });
  }

  const offsets = matchOffsets(text, query);
  const ranges: Range[] = [];
  let partIndex = 0;
  for (const match of offsets) {
    while (partIndex < parts.length && parts[partIndex].end <= match.start) partIndex++;
    const first = parts[partIndex];
    if (!first) break;
    let lastIndex = partIndex;
    while (lastIndex < parts.length && parts[lastIndex].end < match.end) lastIndex++;
    const last = parts[lastIndex];
    if (!last) break;
    const range = document.createRange();
    range.setStart(first.node, match.start - first.start);
    range.setEnd(last.node, match.end - last.start);
    ranges.push(range);
  }
  return ranges;
}

function mutationIsOnlyFindUi(records: MutationRecord[]): boolean {
  return records.every((record) => {
    const el = record.target.nodeType === Node.ELEMENT_NODE ? (record.target as Element) : record.target.parentElement;
    return !!el?.closest(".pane-find");
  });
}

export function PaneFind({
  rootRef,
  active,
  enabled,
}: {
  rootRef: RefObject<HTMLDivElement | null>;
  active: boolean;
  enabled: boolean;
}) {
  const tr = useT();
  const inputRef = useRef<HTMLInputElement>(null);
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const ownerRef = useRef(Symbol("paneFind"));
  const rangesRef = useRef<Range[]>([]);
  const currentRef = useRef(0);
  const fallbackSelectionRef = useRef(false);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [current, setCurrent] = useState(0);
  const [count, setCount] = useState(0);

  const clearHighlights = useCallback(() => {
    if (highlightOwner !== ownerRef.current) return;
    const api = highlightAPI();
    api?.registry.delete(MATCH_HIGHLIGHT);
    api?.registry.delete(CURRENT_HIGHLIGHT);
    highlightOwner = null;
  }, []);

  const paintHighlights = useCallback(
    (index: number, scroll: boolean) => {
      const ranges = rangesRef.current;
      const api = highlightAPI();
      if (api) {
        highlightOwner = ownerRef.current;
        api.registry.set(MATCH_HIGHLIGHT, new api.Highlight(...ranges));
        if (ranges[index]) api.registry.set(CURRENT_HIGHLIGHT, new api.Highlight(ranges[index]));
        else api.registry.delete(CURRENT_HIGHLIGHT);
      }
      if (!api && !ranges[index] && fallbackSelectionRef.current) {
        window.getSelection()?.removeAllRanges();
        fallbackSelectionRef.current = false;
      }
      if (!ranges[index] || !scroll) return;
      const el = ranges[index].startContainer.parentElement;
      el?.scrollIntoView({ block: "center", inline: "nearest" });
      // A selection gives older browsers a visible current result even when the
      // Custom Highlight API is absent. Avoid disturbing selection on modern ones.
      if (!api) {
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(ranges[index]);
        fallbackSelectionRef.current = true;
      }
    },
    [],
  );

  const refresh = useCallback((scroll = false) => {
    const root = rootRef.current;
    const ranges = root && query ? findRanges(root, query) : [];
    rangesRef.current = ranges;
    setCount(ranges.length);
    const next = ranges.length ? Math.min(currentRef.current, ranges.length - 1) : 0;
    currentRef.current = next;
    setCurrent(next);
    paintHighlights(next, scroll);
  }, [paintHighlights, query, rootRef]);

  const close = useCallback(
    (restoreFocus = false) => {
      setOpen(false);
      rangesRef.current = [];
      setCount(0);
      clearHighlights();
      if (fallbackSelectionRef.current) {
        window.getSelection()?.removeAllRanges();
        fallbackSelectionRef.current = false;
      }
      if (restoreFocus) requestAnimationFrame(() => restoreFocusRef.current?.focus());
    },
    [clearHighlights],
  );

  const show = useCallback(() => {
    if (!open) restoreFocusRef.current = document.activeElement as HTMLElement | null;
    setOpen(true);
    requestAnimationFrame(() => inputRef.current?.select());
  }, [open]);

  useEffect(() => {
    if (!active || !enabled) {
      if (open) close();
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (!(event.ctrlKey || event.metaKey) || event.altKey || event.key.toLowerCase() !== "f") return;
      // Do not steal Ctrl-F from the file tree or another focused surface merely
      // because this remains the layout's active pane. Body means no specific
      // control owns focus, in which case the active pane is the useful scope.
      const root = rootRef.current;
      const target = event.target;
      if (target instanceof Node && target !== document && target !== document.body && !root?.contains(target)) return;
      // CodeMirror (the edit pane) has its own search panel (searchKeymap), and because it is
      // virtualised PaneFind could only scan the visible lines — so Ctrl-F inside the editor is
      // passed through rather than captured.
      if (target instanceof Element && target.closest(".cm-editor")) return;
      event.preventDefault();
      event.stopPropagation();
      show();
    };
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, [active, close, enabled, open, show]);

  useLayoutEffect(() => {
    if (!open) return;
    refresh(true);
  }, [open, query, refresh]);

  // Mirror/Chat append content, FileView loads asynchronously, and diff folds change
  // the searchable DOM. Refresh after those changes so counts never go stale.
  useEffect(() => {
    const root = rootRef.current;
    if (!open || !root) return;
    let frame = 0;
    const observer = new MutationObserver((records) => {
      if (mutationIsOnlyFindUi(records)) return;
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => refresh());
    });
    observer.observe(root, { childList: true, characterData: true, subtree: true });
    return () => {
      observer.disconnect();
      cancelAnimationFrame(frame);
    };
  }, [open, refresh, rootRef]);

  useEffect(
    () => () => {
      clearHighlights();
      if (fallbackSelectionRef.current) window.getSelection()?.removeAllRanges();
    },
    [clearHighlights],
  );

  const move = (delta: number) => {
    if (!count) return;
    const next = (currentRef.current + delta + count) % count;
    currentRef.current = next;
    setCurrent(next);
    paintHighlights(next, true);
  };

  if (!open) return null;
  return (
    <div className="pane-find" role="search" onMouseDown={(event) => event.stopPropagation()}>
      <input
        ref={inputRef}
        value={query}
        onChange={(event) => {
          setQuery(event.target.value);
          currentRef.current = 0;
          setCurrent(0);
        }}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            move(event.shiftKey ? -1 : 1);
          } else if (event.key === "Escape") {
            event.preventDefault();
            close(true);
          }
        }}
        placeholder={tr("ui.find_in_pane")}
        aria-label={tr("ui.find_in_pane")}
        autoComplete="off"
        spellCheck={false}
      />
      <span className="pane-find-count" aria-live="polite">
        {query ? (count ? `${current + 1}/${count}` : "0/0") : ""}
      </span>
      <IconButton
        icon="chevron-up"
        label={tr("ui.find_prev")}
        disabled={!count}
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => move(-1)}
      />
      <IconButton
        icon="chevron-down"
        label={tr("ui.find_next")}
        disabled={!count}
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => move(1)}
      />
      <IconButton
        icon="close"
        label={tr("ui.close_find")}
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => close(true)}
      />
    </div>
  );
}
