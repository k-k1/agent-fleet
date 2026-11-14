// The release card (ADR 0137 §3): the held messages one session (or /mcp connection) put into
// one auto-run category. One press approves them, each with the content hash the row showed,
// so a message that changed after the card was drawn is refused rather than approved unseen.
//
// The card draws only what it is given and sends exactly that. It reads no store of its own:
// the memo section re-renders it from the live list on every poll, so an edit made from
// inside the workspace shows up here with its new hash before it can be approved.
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { MemoImageThumb } from "./MemoImageThumb.tsx";
import { useT } from "../../lib/i18n/index.ts";
import type { Memo } from "../../types/memo.ts";
import type { ReleaseCard } from "./queues.ts";

/** What a row shows of a message: the whole body, never a cut. The release is bound to a hash of
 *  the full content, so anything shorter would let a change past the cut be approved unseen.
 *  The reference path is shown on its own line, since it is hashed too. */
export function releaseRowText(m: Memo): string {
  return m.body || "";
}

interface Props {
  card: ReleaseCard;
  busy: boolean;
  /** Ids a refused release named as changed; marked until the card is approved. */
  changedIds: ReadonlySet<string>;
  onRelease: (items: Memo[]) => void;
}

export function MemoReleaseCard({ card, busy, changedIds, onRelease }: Props) {
  const tr = useT();
  const title =
    card.provenance === "mcp" && card.producer
      ? tr("memo.release_title_mcp", { name: card.producer })
      : card.producer
        ? tr("memo.release_title_session", { name: card.producer })
        : tr("memo.release_title_unknown");
  const anyChanged = card.items.some((m) => changedIds.has(m.id));
  return (
    <div className={"memo-release" + (anyChanged ? " changed" : "")} role="group" aria-label={title}>
      <div className="memo-release-head">
        <Icon name="shield" />
        <span className="memo-release-title">{title}</span>
      </div>
      <ul className="memo-release-items">
        {card.items.map((m) => (
          <li key={m.id} className="memo-release-item">
            <div className="memo-release-body">
              {m.kind === "file" && m.refPath && <code className="memo-release-ref">{m.refPath}</code>}
              <div className="memo-release-text">{releaseRowText(m) || (m.kind === "file" ? "" : "—")}</div>
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
            {changedIds.has(m.id) && <span className="memo-release-changed">{tr("memo.changed_tag")}</span>}
          </li>
        ))}
      </ul>
      {anyChanged && (
        <div className="memo-release-note" role="alert">
          {tr("memo.release_stale")}
        </div>
      )}
      <div className="memo-release-foot">
        <span className="memo-release-hint">{tr("memo.release_hint")}</span>
        <Button small variant="primary" disabled={busy} onClick={() => onRelease(card.items)}>
          {busy ? tr("memo.release_busy") : tr("memo.release_button", { count: card.items.length })}
        </Button>
      </div>
    </div>
  );
}
