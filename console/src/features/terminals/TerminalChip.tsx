// The count chip a repository or space row of the Sessions view carries for the shells opened in
// it (ADR 0142 decision 10). The Sessions view lists no shell, so the chip is how a place's
// shells stay one click away: it switches to the Terminal view and reveals that place's group
// (the left-pane switch is wireRailReveals' business, as for every other reveal).
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useTerminalReveal } from "./store.ts";

export function TerminalChip({ count, groupKey }: { count: number; groupKey: string }) {
  const tr = useT();
  if (count <= 0) return null;
  const label = tr("terms.chip_title", { n: count });
  return (
    <button
      type="button"
      className="term-chip"
      title={label}
      aria-label={label}
      // The row around it folds or opens on a click; the chip only travels.
      onClick={(e) => {
        e.stopPropagation();
        useTerminalReveal.getState().reveal(groupKey);
      }}
      onKeyDown={(e) => e.stopPropagation()}
    >
      <Icon name="terminal" />
      {count}
    </button>
  );
}
