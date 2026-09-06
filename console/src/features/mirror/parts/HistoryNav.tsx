import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";

/** History nav for phones (no arrow keys); hidden on wider screens via CSS. */
export function HistoryNav({
  canPrev,
  canNext,
  onPrev,
  onNext,
}: {
  canPrev: boolean;
  canNext: boolean;
  onPrev: () => void;
  onNext: () => void;
}) {
  return (
    <div className="mirror-hist">
      <button type="button" className="ghost mirror-hist-btn" title={tr("mirror.prev_input")} disabled={!canPrev} onClick={onPrev}>
        <Icon name="chevron-up" />
      </button>
      <button type="button" className="ghost mirror-hist-btn" title={tr("mirror.next_input")} disabled={!canNext} onClick={onNext}>
        <Icon name="chevron-down" />
      </button>
    </div>
  );
}

/**
 * Opens the Ctrl+R history search from a tap. Phones only (`.mirror-hsearch-btn` is display:none
 * above the phone width): they have the history column but no Ctrl key, and stacking a third
 * button into that column would grow the whole composer row, so this sits with + and / instead.
 */
export function HistorySearchButton({ open, disabled, onOpen }: { open: boolean; disabled: boolean; onOpen: () => void }) {
  return (
    <button
      type="button"
      className={"ghost mirror-hsearch-btn" + (open ? " on" : "")}
      title={tr("mirror.hsearch_open")}
      disabled={disabled}
      onClick={onOpen}
    >
      <Icon name="search" />
    </button>
  );
}
