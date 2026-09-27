// DraftBar — the draft as one line above the conversation's composer, on a narrow pane only
// (the three columns show the draft itself). It is what lets a phone run the studio's loop
// without leaving the conversation tab: what the prompt says now, the model, trial and
// enqueue, and the latest trial's thumbnail. Tapping the bar anywhere but its buttons opens
// the settings tab.
//
// The buttons follow the form's own rule (pressBlocked) and call the same submit the form
// does, so a press here and a press there are one press.
import type { MouseEvent as RMouseEvent } from "react";
import { downloadURL } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import type { ImagegenDraft } from "../draft.ts";
import { pressBlocked, promptSummary, type PressGate } from "../loop.ts";
import type { ResultItem } from "./ResultCards.tsx";

/** The thumbnail size every other card asks for — the same cache key. */
const THUMB = 512;

export function DraftBar({
  draft,
  modelName,
  highlight,
  trial,
  gate,
  onTrial,
  onEnqueue,
  onOpenForm,
  onZoom,
}: {
  draft: ImagegenDraft;
  /** The chosen model's display name; empty when none is chosen. */
  modelName: string;
  /** The agent changed the draft and the member has not looked (the form's outline). */
  highlight: boolean;
  trial: ResultItem | null;
  gate: PressGate;
  onTrial: () => void;
  onEnqueue: () => void;
  onOpenForm: () => void;
  onZoom: (path: string) => void;
}) {
  const tr = useT();
  const blocked = pressBlocked(draft, gate);
  const summary = promptSummary(draft.prompt);
  // Buttons act on their own; the rest of the bar is one large "open the settings" target.
  const stop = (e: RMouseEvent) => e.stopPropagation();
  return (
    <div
      className={"igen-draftbar" + (highlight ? " igen-draftbar-hl" : "")}
      onClick={onOpenForm}
      role="group"
      aria-label={tr("imggen.bar_label")}
    >
      {trial && (
        <button
          type="button"
          className="igen-draftbar-thumb"
          title={tr("imggen.trial_latest")}
          onClick={(e) => {
            stop(e);
            onZoom(trial.file.path);
          }}
        >
          <img src={downloadURL(trial.file.path, THUMB)} alt={tr("imggen.trial_latest")} loading="lazy" decoding="async" />
        </button>
      )}
      {/* No handler of its own: its click bubbles to the bar's. A button so the keyboard reaches it. */}
      <button type="button" className="igen-draftbar-text" title={tr("imggen.bar_open_form")}>
        <span className={"igen-draftbar-prompt" + (summary ? "" : " muted")}>{summary || tr("imggen.bar_empty")}</span>
        <span className="igen-draftbar-model muted">
          {highlight && <span className="igen-draftbar-dot" aria-label={tr("imggen.hl_note")} />}
          {modelName || tr("imggen.bar_no_model")}
        </span>
      </button>
      <button
        type="button"
        className="ui-btn igen-draftbar-btn"
        disabled={blocked.trial}
        title={gate.trialFull ? tr("imggen.trial_full") : tr("imggen.trial_title")}
        onClick={(e) => {
          stop(e);
          onTrial();
        }}
      >
        <Icon name="beaker" /> {tr("imggen.trial")}
      </button>
      <button
        type="button"
        className="ui-btn ui-btn-primary igen-draftbar-btn"
        disabled={blocked.enqueue}
        title={gate.queueFull ? tr("imggen.queue_full") : tr("imggen.enqueue_title", { n: draft.jobs })}
        onClick={(e) => {
          stop(e);
          onEnqueue();
        }}
      >
        <Icon name="play" /> {tr("imggen.bar_enqueue", { n: draft.jobs })}
      </button>
    </div>
  );
}
