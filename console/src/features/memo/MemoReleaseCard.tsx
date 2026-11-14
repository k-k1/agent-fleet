// The release card (ADR 0137 §3): the held messages one session (or /mcp connection) put into
// one auto-run category. One press approves them, each with the content hash the row showed,
// so a message that changed after the card was drawn is refused rather than approved unseen.
//
// The card draws only what it is given and sends exactly that. It reads no store of its own:
// the memo section re-renders it from the live list on every poll, so an edit made from
// inside the workspace shows up here with its new hash before it can be approved.
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import type { Memo } from "../../types/memo.ts";
import type { ReleaseCard } from "./queues.ts";

const PREVIEW = 240;
const preview = (s: string): string => {
  const one = s.replace(/\s+/g, " ").trim();
  return one.length > PREVIEW ? one.slice(0, PREVIEW - 1) + "…" : one;
};

/** What a row shows of a message: plain text, never JSON. A file reference leads with its path. */
export function releaseRowText(m: Memo): string {
  const body = preview(m.body || "");
  if (m.kind === "file") return m.refPath ? (body ? `${m.refPath} — ${body}` : m.refPath) : body;
  return body;
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
            <span className="memo-release-text">{releaseRowText(m) || "—"}</span>
            {(m.attachments?.length ?? 0) > 0 && (
              <span className="memo-release-att" aria-hidden="true">
                <Icon name="device-camera" /> {m.attachments!.length}
              </span>
            )}
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
