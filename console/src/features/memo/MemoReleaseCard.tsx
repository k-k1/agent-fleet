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
import { MemoProducerText } from "./MemoProducerText.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { MemoImageThumb } from "./MemoImageThumb.tsx";
import { MemoReferenceLink } from "./MemoReferenceLink.tsx";
import { memoReference } from "./reference.ts";
import { tidyMarks } from "./queues.ts";
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

export function MemoReleaseCard({ card, busy, changedIds, edited, awaiting, onRelease, onConfirm }: Props) {
  const tr = useT();
  const title =
    card.provenance === "mcp" && card.producer
      ? tr("memo.release_title_mcp", { name: card.producer })
      : card.producer
        ? tr("memo.release_title_session", { name: card.producer })
        : tr("memo.release_title_unknown");
  const marked = (id: string) => changedIds.has(id) || edited.has(id);
  const anyChanged = card.items.some((m) => marked(m.id));
  const anyEdited = card.items.some((m) => edited.has(m.id));
  const mustConfirm = card.items.some((m) => awaiting.has(m.id));
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
              <div className="memo-release-body">
                {m.kind === "file" && m.refPath && <code className="memo-release-ref">{m.refPath}</code>}
                {ref && <MemoReferenceLink reference={ref} />}
                {tidyMarks(m).map((k) => (
                  <div key={k.key} className="memo-release-tidy" title={k.title ? tr(k.title) : undefined}>
                    {tr(k.key, k.params)}
                  </div>
                ))}
                {(releaseRowText(m) || (m.kind !== "file" && !ref)) && (
                  <div className="memo-release-text">{releaseRowText(m) || "—"}</div>
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
          {card.items.some((m) => memoReference(m)) && " " + tr("memo.ref_card_hint")}
          {card.items.some((m) => m.tidyFrom?.id) && " " + tr("memo.release_tidy_hint")}
        </span>
        {mustConfirm ? (
          <Button small variant="primary" disabled={busy} onClick={() => onConfirm(card.items)}>
            {tr("memo.release_confirm")}
          </Button>
        ) : (
          <Button small variant="primary" disabled={busy} onClick={() => onRelease(card.items)}>
            {busy ? tr("memo.release_busy") : tr("memo.release_button", { count: card.items.length })}
          </Button>
        )}
      </div>
    </div>
  );
}
