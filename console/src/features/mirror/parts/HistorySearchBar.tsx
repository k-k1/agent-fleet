import type { KeyboardEvent as RKeyboardEvent, Ref } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";

/**
 * Ctrl+R history search bar: a full-width band above the composer input (bash's
 * `(reverse-i-search)` prompt). The match itself is not previewed here — it is written into the
 * composer below, where a multi-line prompt is readable in full — so this row carries only the
 * query, where in the history it sits, and how to leave. Driven by parts/useHistorySearch.
 */
export function HistorySearchBar({
  inputRef,
  query,
  count,
  pos,
  failed,
  onQuery,
  onKeyDown,
  onBlur,
  onCancel,
}: {
  inputRef: Ref<HTMLInputElement>;
  query: string;
  count: number;
  /** 1-based position in the match list; 0 while nothing is previewed yet. */
  pos: number;
  failed: boolean;
  onQuery: (v: string) => void;
  onKeyDown: (e: RKeyboardEvent) => void;
  onBlur: () => void;
  onCancel: () => void;
}) {
  return (
    <div className={"mirror-histsearch" + (failed ? " failed" : "")}>
      <Icon name="search" />
      <span className="mirror-histsearch-label">{tr(failed ? "mirror.hsearch_failed" : "mirror.hsearch_label")}</span>
      <input
        ref={inputRef}
        className="mirror-histsearch-input"
        type="text"
        value={query}
        placeholder={tr("mirror.hsearch_ph")}
        aria-label={tr("mirror.hsearch_label")}
        onChange={(e) => onQuery(e.target.value)}
        onKeyDown={onKeyDown}
        onBlur={onBlur}
      />
      <span className="mirror-histsearch-count">{pos > 0 ? pos + "/" + count : String(count)}</span>
      <span className="muted mirror-histsearch-hint">{tr("mirror.hsearch_hint")}</span>
      {/* preventDefault on mousedown keeps the focus in the field: without it the blur lands
          first, ends the search by accepting, and this button's click never happens. */}
      <button
        type="button"
        className="ghost mirror-histsearch-close"
        title={tr("mirror.hsearch_cancel")}
        onMouseDown={(e) => e.preventDefault()}
        onClick={onCancel}
      >
        <Icon name="close" />
      </button>
    </div>
  );
}
