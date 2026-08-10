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

// 分岐の 2 通り。同じ操作が 1 往復ずれているだけなので、モーダルの中で切り替える。
//  redo    … この発言の直前まで（＝この発言を打ち直す）。既定。
//  continue… この発言と、それが得た回答まで引き継ぐ（＝続きから別方向へ）。
type ForkMode = "redo" | "continue";

export function ForkAtModal({
  session,
  target,
  onDone,
  onClose,
}: {
  session: string;
  target: ForkAtTarget;
  // 分岐に成功したとき、生まれたセッション名とモードを渡す（ペインを開き、下書きを入れるかを
  // 決めるのは呼び出し側の仕事）。
  onDone: (name: string, opts: { draft: string }) => void;
  onClose: () => void;
}) {
  const tr = useT();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [mode, setMode] = useState<ForkMode>("redo");

  const preview = target.text.length > PREVIEW_CHARS ? target.text.slice(0, PREVIEW_CHARS) + "…" : target.text;

  const run = async () => {
    if (busy) return;
    setBusy(true);
    setErr("");
    try {
      const d = await apiJSON(`api/sessions/${encodeURIComponent(session)}/fork`, "POST", {
        at: target.anchorId,
        include: mode === "continue",
      });
      // api() は失敗しても throw せず {error:{code,message}} を返す（client.ts）。ここで
      // 先に見ないと、サーバが返した理由（分岐点が使えない・上限・起動方式）が全部下の
      // 汎用メッセージに化け、失敗しても原因が分からない画面になる。
      if (d?.error) throw d.error as ApiError;
      if (!d?.name) throw new Error("no session in fork response");
      // 下書きを入れるのは「打ち直す」ときだけ。「続きから」ではその発言は分岐先に残って
      // いるので、入力欄に同じ文が現れたら二重に見える。
      onDone(d.name as string, { draft: mode === "redo" ? target.text : "" });
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

        <div className="ui-field">
          <span className="ui-field-label">{tr("mirror.fork_at_mode")}</span>
          {/* 同じ操作が 1 往復ずれているだけなので、並べて選ばせる。既定は「打ち直す」——
              方針を間違えた直後がいちばん多い用途で、そこでは分岐点の発言も捨てたい。 */}
          <div className="ui-seg big" role="radiogroup" aria-label={tr("mirror.fork_at_mode")}>
            {(["redo", "continue"] as const).map((k) => (
              <button
                key={k}
                type="button"
                role="radio"
                aria-checked={mode === k}
                className={"seg-btn" + (mode === k ? " active" : "")}
                onClick={() => setMode(k)}
                disabled={busy}
              >
                {tr(k === "redo" ? "mirror.fork_at_mode_redo" : "mirror.fork_at_mode_continue")}
                <span className="seg-sub">
                  {tr(k === "redo" ? "mirror.fork_at_mode_redo_hint" : "mirror.fork_at_mode_continue_hint")}
                </span>
              </button>
            ))}
          </div>
        </div>

        <ul className="mirror-fork-facts">
          <li>
            {mode === "redo"
              ? tr("mirror.fork_at_carried", { count: String(target.carried) })
              : tr("mirror.fork_at_carried_incl", { count: String(target.carried + 1) })}
          </li>
          <li>{tr("mirror.fork_at_keeps_source")}</li>
          {mode === "redo" && <li>{tr("mirror.fork_at_draft")}</li>}
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
