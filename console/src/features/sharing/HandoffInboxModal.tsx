// HandoffInboxModal — 受け取った引き継ぎの受信箱（docs/log/77 / ADR 0057）。
//
// 行そのものは HandoffOfferRow（共有ビューの帯からも同じ面を開く）。ここが持つのは
// 「どれを出すか」だけ: 既定は未処理の全件で、`offerId` を渡すとその 1 件に絞る
// —— 共有ビューの帯や通知から来た人に、他人の引き継ぎまで並べても仕方がないため。
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
