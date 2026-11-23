// The release card (ADR 0137 §3): the held messages one session (or /mcp connection) put into
// one auto-run category. One press approves them, each with the content hash the row showed,
// so a message that changed after the card was drawn is refused rather than approved unseen.
//
// The card draws only what it is given and sends exactly that. It reads no memo store of its own:
// the memo section re-renders it from the live list on every poll, so an edit made from
// inside the workspace shows up here with its new hash. Because the text then changes under a
// person who may be about to press, the section also says which notes changed after the card first
// showed them (`edited`), and which of those the person has still to confirm (`awaiting`): while
// any is awaiting, the button confirms instead of approving. A card that never changed approves
// in one press.
//
// Each row of a card with more than one note carries a checkbox, all ticked until the person says
// otherwise: the approval covers exactly the ticked notes, each with its own hash, and the rest
// stay held on the card. Whether a note is unticked is kept by id, so a note that arrives on a later
// poll starts ticked like the others, as the card shows it.
import { useState } from "react";
import { MemoProducerText } from "./MemoProducerText.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { MemoImageThumb } from "./MemoImageThumb.tsx";
import { MemoReferenceLink } from "./MemoReferenceLink.tsx";
import { memoReference } from "./reference.ts";
import { MemoRecipeBadge } from "./MemoRecipe.tsx";
import { continuationHeldKey, heldRecipeKey, fmtStartAfter, scheduledUntil, tidyMarks } from "./queues.ts";
import { useT } from "../../lib/i18n/index.ts";
import type { Memo } from "../../types/memo.ts";
import type { ReleaseCard } from "./queues.ts";

/** What a row shows of a message: the whole body, never a cut. The release is bound to a hash of
 *  the full content, so anything shorter would let a change past the cut be approved unseen.
 *  The reference path and the work item a note points at are shown on lines of their own, since
 *  both are hashed too: a card never hides a reference. */
export function releaseRowText(m: Memo): string {
  return m.body || "";
}

interface Props {
  card: ReleaseCard;
  /** The category's follow-up allowance (0 or absent: none), said on the card because approving covers it. */
  continuationMax?: number;
  busy: boolean;
  /** Ids a refused release named as changed; marked until the card is approved. */
  changedIds: ReadonlySet<string>;
  /** Notes whose content changed after this card first showed them: marked until approved. */
  edited: ReadonlySet<string>;
  /** The edited notes whose current content the person has not confirmed yet. */
  awaiting: ReadonlySet<string>;
  onRelease: (items: Memo[]) => void;
  /** The person has read the change: the next press approves. */
  onConfirm: (items: Memo[]) => void;
}

export function MemoReleaseCard({ card, continuationMax, busy, changedIds, edited, awaiting, onRelease, onConfirm }: Props) {
  const tr = useT();
  const title =
    card.provenance === "mcp" && card.producer
      ? tr("memo.release_title_mcp", { name: card.producer })
      : card.producer
        ? tr("memo.release_title_session", { name: card.producer })
        : tr("memo.release_title_unknown");
  const [unticked, setUnticked] = useState<ReadonlySet<string>>(new Set());
  const total = card.items.length;
  // A lone note has no checkbox, so a tick left over from when the card held more must not count:
  // otherwise the last note could be neither approved nor ticked again.
  const pickable = total > 1;
  const isTicked = (id: string) => !pickable || !unticked.has(id);
  const ticked = card.items.filter((m) => isTicked(m.id));
  const toggle = (id: string) =>
    setUnticked((s) => {
      const n = new Set(s);
      if (!n.delete(id)) n.add(id);
      return n;
    });
  const marked = (id: string) => changedIds.has(id) || edited.has(id);
  const anyChanged = card.items.some((m) => marked(m.id));
  // Only what the approval would cover decides whether it needs a confirming press first.
  const anyEdited = ticked.some((m) => edited.has(m.id));
  const mustConfirm = ticked.some((m) => awaiting.has(m.id));
  return (
    <div className={"memo-release" + (anyChanged ? " changed" : "")} role="group" aria-label={title}>
      <div className="memo-release-head">
        <Icon name="shield" />
        <span className="memo-release-title">
          {card.provenance === "workspace" && card.producer ? (
            <MemoProducerText k="memo.release_title_session" name={card.producer} link />
          ) : (
            title
          )}
        </span>
      </div>
      <ul className="memo-release-items">
        {card.items.map((m) => {
          const ref = memoReference(m);
          return (
            <li key={m.id} className="memo-release-item">
              {pickable && (
                <input
                  type="checkbox"
                  className="memo-release-check"
                  checked={isTicked(m.id)}
                  disabled={busy}
                  aria-label={tr("memo.release_pick")}
                  onChange={() => toggle(m.id)}
                />
              )}
              <div className="memo-release-body">
                {m.kind === "file" && m.refPath && <code className="memo-release-ref">{m.refPath}</code>}
                {ref && <MemoReferenceLink reference={ref} />}
                {tidyMarks(m).map((k) => (
                  <div key={k.key} className="memo-release-tidy" title={k.title ? tr(k.title) : undefined}>
                    {tr(k.key, k.params)}
                  </div>
                ))}
                {continuationHeldKey(m.heldReason?.reason) && (
                  <div className="memo-release-tidy">{tr("memo.continuation_held", { why: tr(continuationHeldKey(m.heldReason?.reason)!) })}</div>
                )}
                {heldRecipeKey(m.heldReason?.reason) && <div className="memo-release-tidy">{tr(heldRecipeKey(m.heldReason?.reason)!)}</div>}
                {/* The recipe is hashed with the content, so an approval covers it and the card shows it. */}
                <MemoRecipeBadge memo={m} className="memo-release-recipe" />
                {(releaseRowText(m) || (m.kind !== "file" && !ref)) && (
                  <div className="memo-release-text">{releaseRowText(m) || "—"}</div>
                )}
                {/* What it waits for is part of what an approval covers, so the card shows it. */}
                {(m.after?.length ?? 0) > 0 && (
                  <div className="memo-release-after">
                    {tr("memo.after_label")}: {m.after!.map((id) => m.afterStatus?.find((a) => a.id === id)?.label || id).join(", ")}
                  </div>
                )}
                {/* So is its start time: the release keeps it. */}
                {scheduledUntil(m) && (
                  <div className="memo-release-after">{tr("memo.start_until", { at: fmtStartAfter(scheduledUntil(m)!) })}</div>
                )}
                {(m.attachments?.length ?? 0) > 0 && (
                  <div className="memo-release-atts">
                    {m.attachments!.map((a) => (
                      <span key={a.name} className="memo-release-att" title={a.name}>
                        <MemoImageThumb name={a.name} />
                        <span className="memo-release-att-name">{a.name}</span>
                      </span>
                    ))}
                  </div>
                )}
              </div>
              {marked(m.id) && <span className="memo-release-changed">{tr("memo.changed_tag")}</span>}
            </li>
          );
        })}
      </ul>
      {card.items.some((m) => changedIds.has(m.id)) && (
        <div className="memo-release-note" role="alert">
          {tr("memo.release_stale")}
        </div>
      )}
      {anyEdited && (
        <div className="memo-release-note" role="alert">
          {tr(mustConfirm ? "memo.release_edited" : "memo.release_confirmed")}
        </div>
      )}
      <div className="memo-release-foot">
        <span className="memo-release-hint">
          {tr("memo.release_hint")}
          {ticked.length < total && " " + tr("memo.release_partial_hint")}
          {card.items.some((m) => memoReference(m)) && " " + tr("memo.ref_card_hint")}
          {card.items.some((m) => m.tidyFrom?.id) && " " + tr("memo.release_tidy_hint")}
          {(continuationMax ?? 0) > 0 && card.items.some((m) => memoReference(m)) && " " + tr("memo.release_continuation_hint", { max: continuationMax! })}
        </span>
        {mustConfirm ? (
          <Button small variant="primary" disabled={busy || ticked.length === 0} onClick={() => onConfirm(ticked)}>
            {tr("memo.release_confirm")}
          </Button>
        ) : (
          <Button small variant="primary" disabled={busy || ticked.length === 0} onClick={() => onRelease(ticked)}>
            {busy
              ? tr("memo.release_busy")
              : ticked.length < total
                ? tr("memo.release_button_partial", { count: ticked.length, total })
                : tr("memo.release_button", { count: total })}
          </Button>
        )}
      </div>
    </div>
  );
}
