// HandoffInboxModal is the inbox of handoffs received (docs/log/77 / ADR 0057).
//
// The rows themselves are HandoffOfferRow (the banner in the shared view opens the same
// surface). All this component decides is WHICH offers to show: by default every
// unprocessed one, or just the one named by `offerId` — someone arriving from a banner or
// a notification has no use for other people's handoffs.
import { Modal } from "../../ui/Modal.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { HandoffOfferRow } from "./HandoffOfferRow.tsx";
import { useHandoffStore } from "./handoffStore.ts";
import "./sharing.css";

export function HandoffInboxModal({ onClose, offerId }: { onClose: () => void; offerId?: string }) {
  const tr = useT();
  const received = useHandoffStore((s) => s.received);
  const offers = offerId ? received.filter((o) => o.id === offerId) : received;
  return (
    <Modal title={tr("handoff.inbox_title")} onClose={onClose}>
      <div className="ui-modal-body">
        {offers.length === 0 ? (
          <p className="ui-field-hint">{tr("handoff.inbox_empty")}</p>
        ) : (
          <ul className="handoff-inbox">
            {offers.map((o) => (
              <HandoffOfferRow key={o.id} offer={o} onDone={onClose} />
            ))}
          </ul>
        )}
      </div>
    </Modal>
  );
}
