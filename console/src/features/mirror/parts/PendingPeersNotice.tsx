import { Icon } from "../../../ui/Icon.tsx";
import { t as tr, tCount } from "../../../lib/i18n/index.ts";
import type { PendingPeer } from "../../../core/api/client.ts";

/** parsePendingPeers keeps the well-formed entries of a messages payload's `pendingPeers`. */
export function parsePendingPeers(v: unknown): PendingPeer[] {
  if (!Array.isArray(v)) return [];
  return v.filter(
    (p): p is PendingPeer =>
      !!p && typeof p === "object" && typeof (p as PendingPeer).id === "string" && typeof (p as PendingPeer).from === "string",
  );
}

/** PendingPeersNotice says which peer messages wait for the member's answer (#1031): the Agent
 *  holds them while a question, plan or permission prompt is up and delivers them once the
 *  turn the answer starts has ended. One line per sender, in arrival order, and each message
 *  can be dropped so it is never delivered. */
export function PendingPeersNotice({ items, onDrop }: { items: PendingPeer[]; onDrop: (id: string) => void }) {
  if (!items.length) return null;
  const bySender = new Map<string, PendingPeer[]>();
  for (const p of items) bySender.set(p.from, [...(bySender.get(p.from) ?? []), p]);
  return (
    <div className="mirror-peer-pending" role="status">
      {[...bySender].map(([from, msgs]) => (
        <div className="mpp-group" key={from}>
          <div className="mpp-head">
            <Icon name="mail" />
            <span className="mpp-msg">{tCount("mirror.peer_pending", msgs.length, { name: from })}</span>
          </div>
          <ul className="mpp-items">
            {msgs.map((m) => (
              <li className="mpp-item" key={m.id}>
                <span className="mpp-text" title={m.excerpt}>
                  {m.excerpt}
                </span>
                <button
                  type="button"
                  className="ghost mpp-drop"
                  title={tr("mirror.peer_pending_drop_title")}
                  onClick={() => onDrop(m.id)}
                >
                  {tr("mirror.peer_pending_drop")}
                </button>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}
