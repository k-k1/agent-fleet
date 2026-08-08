// ForkAtModal — 「ここから分岐」の確認ダイアログ（docs/55）。ミラーの過去のユーザー発言から、
// そこまでの文脈を引き継いだ新セッションを起こす。
//
// 引き継ぎ（HandoffModal）とは別物で、そこが分かれ目なので本文で言い切る: 引き継ぎは会話を
// LLM に要約させて別エージェントへ渡し、分岐は同じエージェントで会話をそのまま複製する。
// 「元は残る」ことも明示する — 分岐が破壊的だと思われると、いちばん使ってほしい場面
// （方針を間違えた直後）で押してもらえない。
import { useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { apiJSON, errText } from "../../core/api/client.ts";
import type { ApiError } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";

// 確認画面に出す分岐点のプレビュー。全文はミラーで読めるので、どの発言か分かる長さで足りる。
const PREVIEW_CHARS = 240;

export interface ForkAtTarget {
  anchorId: string;
  text: string;
  // このセッションの会話で、分岐先へ引き継がれるユーザー発言の数（分岐点は含まない）。
  carried: number;
}

export function ForkAtModal({
  session,
  target,
  onDone,
  onClose,
}: {
  session: string;
  target: ForkAtTarget;
  // 分岐に成功したとき、生まれたセッション名を渡す（ペインを開くのは呼び出し側の仕事）。
  onDone: (name: string) => void;
  onClose: () => void;
}) {
  const tr = useT();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const preview = target.text.length > PREVIEW_CHARS ? target.text.slice(0, PREVIEW_CHARS) + "…" : target.text;

  const run = async () => {
    if (busy) return;
    setBusy(true);
    setErr("");
    try {
      const d = await apiJSON(`api/sessions/${encodeURIComponent(session)}/fork`, "POST", { at: target.anchorId });
      if (!d?.name) throw new Error("no session in fork response");
      onDone(d.name as string);
      onClose();
    } catch (e) {
      // 失敗しても閉じない: 分岐点が古い（ミラーを再読込すれば直る）ことも、この経路では
      // そもそもできない（fork_at_unsupported）こともあるので、理由を出して判断させる。
      setErr(errText(e as ApiError) || tr("mirror.fork_at_failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={tr("mirror.fork_at_title")} onClose={onClose} lockClose={busy}>
      <div className="ui-modal-body">
        <div className="ui-field-hint">{tr("mirror.fork_at_intro")}</div>

        <div className="ui-field">
          <span className="ui-field-label">{tr("mirror.fork_at_point")}</span>
          <blockquote className="mirror-fork-preview">{preview}</blockquote>
        </div>

        <ul className="mirror-fork-facts">
          <li>{tr("mirror.fork_at_carried", { count: String(target.carried) })}</li>
          <li>{tr("mirror.fork_at_keeps_source")}</li>
          <li>{tr("mirror.fork_at_draft")}</li>
        </ul>

        {err && (
          <div className="managed-settings-error" role="alert">
            <Icon name="warning" /> {err}
          </div>
        )}
      </div>

      <footer className="ui-modal-foot">
        <button type="button" className="ui-btn ui-btn-ghost" onClick={onClose} disabled={busy}>
          {tr("common.cancel")}
        </button>
        <button type="button" className="ui-btn ui-btn-primary" onClick={() => void run()} disabled={busy}>
          {tr("mirror.fork_at_go")}
        </button>
      </footer>
    </Modal>
  );
}
