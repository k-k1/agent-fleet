// "Run now" on the category head of the Memo queue view, next to the run history and the settings.
// A component of its own because the head is drawn in a loop and the control needs hook state.
import { Icon } from "../../ui/Icon.tsx";
import type { MemoQueue } from "../../types/memo.ts";
import { useRunNow, type RunNowResult } from "./runNow.ts";

interface Props {
  categoryId: string;
  queue: MemoQueue | undefined;
  /** How many notes of the category wait to run. */
  waiting: number;
  /** The press ended; the caller refetches on "started" and may explain a "no_slot". */
  onResult: (r: RunNowResult) => void;
}

export function RunNowButton({ categoryId, queue, waiting, onResult }: Props) {
  const run = useRunNow(categoryId, queue, waiting);
  const off = run.reason !== "" || run.busy;
  return (
    <button
      type="button"
      className="memo-cat-act memo-cat-run"
      title={run.title}
      aria-label={run.title}
      // aria-disabled, not disabled: a native disabled button cannot take focus (the reason would
      // be unreachable by keyboard) and swallows the click, which would then fall through to the
      // header and fold the category.
      aria-disabled={off ? true : undefined}
      onClick={(e) => {
        e.stopPropagation();
        if (off) return;
        void run.press().then((r) => r && onResult(r));
      }}
    >
      <Icon name={run.busy ? "loading" : "play"} spin={run.busy} />
    </button>
  );
}
