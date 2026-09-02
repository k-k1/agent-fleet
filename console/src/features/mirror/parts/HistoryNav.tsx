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
