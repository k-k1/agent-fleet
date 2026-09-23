// DraftCallCard — one set_image_draft call in the studio's conversation (ADR 0100 decision 9):
// "Draft updated: cfg 7→5" in place of a faint tool trace, with "back to this point" on the
// edit it wrote. The edit comes from the studio's own log (draftCallEntry), because the
// transcript of most kinds does not carry the tool's result.
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import type { DraftLogEntry } from "../wire.ts";
import { describeChange } from "../studioSync.ts";

export function DraftCallCard({ entry, onRewind }: { entry: DraftLogEntry | null; onRewind: (seq: number) => unknown }) {
  const tr = useT();
  const lines = entry ? (entry.changes || []).flatMap(describeChange) : [];
  return (
    <div className="igen-callcard" data-seq={entry?.seq}>
      <div className="igen-callcard-head">
        <Icon name="edit" />
        <span className="igen-callcard-title">
          {tr("imggen.draft_card")}
          {lines.length > 0 && <>: {lines.join(" · ")}</>}
        </span>
        {entry?.draft && (
          <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={() => void onRewind(entry.seq)}>
            <Icon name="discard" /> {tr("imggen.log_rewind_here")}
          </button>
        )}
      </div>
      {!entry && <p className="igen-hint">{tr("imggen.draft_card_unmatched")}</p>}
    </div>
  );
}
