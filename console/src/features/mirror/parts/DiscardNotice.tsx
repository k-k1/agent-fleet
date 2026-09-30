import { Icon } from "../../../ui/Icon.tsx";
import { t as tr, tCount } from "../../../lib/i18n/index.ts";
import type { Discard, TurnOrigin } from "../../../core/api/client.ts";
import type { DiscardView } from "../stopQueue.ts";

/** originLabel names where a discarded input came from, in the words of the chat's injection
 *  badges, so the notice and the transcript never describe one origin two ways. */
export function originLabel(o: TurnOrigin | undefined): string {
  switch (o?.kind) {
    case "member":
      return tr("mirror.from_you");
    case "discord":
      return tr("mirror.from_chat", { provider: "Discord" });
    case "slack":
      return tr("mirror.from_chat", { provider: "Slack" });
    case "peer":
      return o.from ? tr("mirror.from_peer_named", { name: o.from }) : tr("mirror.from_peer");
    case "spawn":
      return o.from ? tr("mirror.from_spawn_named", { name: o.from }) : tr("mirror.from_spawn");
    case "operator":
      return tr("mirror.from_operator");
    case "schedule":
      return tr("mirror.from_schedule");
    case "schedule-manual":
      return tr("mirror.from_schedule_manual");
    case "auto-resume":
      return tr("mirror.from_auto_resume");
    default:
      return o?.kind || "?";
  }
}

// One line of a discarded input, cut short: the notice says what was dropped, not all of it.
const PREVIEW = 80;
const preview = (s: string): string => {
  const one = s.replace(/\s+/g, " ").trim();
  return one.length > PREVIEW ? one.slice(0, PREVIEW - 1) + "…" : one;
};

/** DiscardNotice tells the member what a second stop (or stop-and-discard) threw away
 *  (ADR 0105 decision 4). Their own input comes back into the input box one message per
 *  press, never sent; input of other origins is only listed, since the member did not write
 *  it. Closing, like restoring the last of the member's entries, tells the driver to drop
 *  the discard. */
export function DiscardNotice({
  notices,
  draftBusy,
  onRestore,
  onClose,
}: {
  notices: { view: DiscardView; restored: number }[];
  /** The input box already holds something: restoring now would overwrite it. */
  draftBusy: boolean;
  onRestore: (d: Discard) => void;
  onClose: (id: string) => void;
}) {
  if (!notices.length) return null;
  return (
    <div className="mirror-discards">
      {notices.map(({ view, restored }) => {
        const { discard, member, others } = view;
        const left = member.length - restored;
        return (
          <div className="mirror-discard" key={discard.id} role="status">
            <div className="md-head">
              <Icon name="trash" />
              <span className="md-msg">
                {/* first_stop: the stop caught input before it reached the runtime (nothing else
                    ran), so nothing was "queued" and "discarded" would misstate it. */}
                {tCount(discard.reason === "first_stop" ? "mirror.discarded_first_stop" : "mirror.discarded", discard.items.length)}
              </span>
              {left > 0 && (
                <button
                  type="button"
                  className="ghost md-restore"
                  disabled={draftBusy}
                  title={draftBusy ? tr("mirror.discarded_restore_busy") : tr("mirror.discarded_restore_title")}
                  onClick={() => onRestore(discard)}
                >
                  <Icon name="discard" />{" "}
                  {member.length > 1
                    ? tr("mirror.discarded_restore_n", { done: restored + 1, total: member.length })
                    : tr("mirror.discarded_restore")}
                </button>
              )}
              <button
                type="button"
                className="ghost md-close"
                title={tr("mirror.discarded_close")}
                aria-label={tr("mirror.discarded_close")}
                onClick={() => onClose(discard.id)}
              >
                <Icon name="close" />
              </button>
            </div>
            <ul className="md-items">
              {member.length > 0 && (
                <li className="md-item md-yours">
                  <span className="md-origin">{tCount("mirror.discarded_yours", member.length)}</span>
                  {left > 0 && <span className="md-text muted">{preview(member[restored].text)}</span>}
                </li>
              )}
              {others.map((it) => (
                <li className="md-item" key={it.id}>
                  <span className="md-origin">{originLabel(it.origin)}</span>
                  <span className="md-text muted">{preview(it.text)}</span>
                </li>
              ))}
            </ul>
          </div>
        );
      })}
    </div>
  );
}
