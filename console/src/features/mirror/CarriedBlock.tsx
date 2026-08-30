// CarriedBlock — 停止時に画面に出ていた対話（docs/log/75 §75.6）。
//
// PendingQuestions（保留カード）との決定的な違いは**答え方**である。保留カードは
// 生きた TUI モーダルへキー列（Down/Enter）を撃つ。持ち越しにはモーダルが無い —
// セッションが畳まれた時点で消え、`claude --resume` しても戻らない（未応答の tool_use は
// 親ポインタで迂回されて会話木から外れる。docs/log/75 §75.10 A で実測）。だから回答は
// **文章として**配達するしかなく、このコンポーネントはキー列を 1 つも組み立てない。
//
// カードを分けてあるのはその不変条件を型で守るため: キーを撃つコード（questionKeys）は
// ここから import していないので、持ち越しのボタンが生きたペインへ Down/Enter を送る
// 経路が構造的に存在しない。
import { useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { t as tr } from "../../lib/i18n/index.ts";
import { sessionCarriedAnswer } from "../../core/api/client.ts";
import type { CarriedInteraction, CarriedAnswerInput } from "../../core/api/client.ts";
import { PendingQuestions } from "./PendingQuestions.tsx";
import { PlanBlock } from "./transcript/blocks.tsx";

export function CarriedBlock({
  carried,
  session,
  agentName,
  onDone,
  onError,
  onOpenPlan,
}: {
  carried: CarriedInteraction;
  session: string;
  agentName: string;
  /** 送信/破棄が受理された（カードを引っ込めてポーリングに任せる）。 */
  onDone: () => void;
  onError: (message: string) => void;
  onOpenPlan?: (plan: string) => void;
}) {
  const [sending, setSending] = useState(false);
  const [feedback, setFeedback] = useState("");

  const send = async (body: Parameters<typeof sessionCarriedAnswer>[1]) => {
    if (sending) return;
    setSending(true);
    const r = await sessionCarriedAnswer(session, body);
    setSending(false);
    // 沈黙は成功と区別が付かない（docs/build/92 §7 の教訓）。失敗は必ずトーストする。
    if (!r.ok) {
      onError(r.message || tr("err.send_failed"));
      return;
    }
    onDone();
  };

  const title =
    carried.kind === "question"
      ? tr("mirror.carried_question")
      : carried.kind === "plan"
        ? tr("mirror.carried_plan")
        : tr("mirror.carried_permission");

  return (
    <div className="mirror-turn assistant mt-carried" data-testid="carried-card">
      <div className="mirror-turn-head">
        <span className="mt-who">{agentName}</span>
        <span className="mt-model muted">{title}</span>
      </div>
      <div className="mirror-turn-body">
        {/* なぜこのカードが出ているのかを最初に言う。利用者から見ると「答えたはずの
            質問がまた出ている」ようにも見えるので、答えが届いていないことと、
            答えれば再開することを説明する。 */}
        <div className="mt-carried-note muted">
          <Icon name="info" /> {tr("mirror.carried_note")}
        </div>
        {carried.text && <div className="mt-carried-text">{carried.text}</div>}
        {carried.kind === "question" && (
          <PendingQuestions
            questions={carried.questions || []}
            sending={sending}
            // キー駆動の入口は塞ぐ。持ち越しには当てる先のモーダルが無い。
            onSubmitKeys={() => onError(tr("mirror.carried_no_keys"))}
            onSubmitSeq={() => onError(tr("mirror.carried_no_keys"))}
            onSubmitAnswers={(answers: CarriedAnswerInput[]) => void send({ decision: "answer", answers })}
            onCancel={() => void send({ decision: "discard" })}
            cancelLabel={tr("mirror.carried_discard")}
            submitLabel={tr("mirror.carried_send")}
          />
        )}
        {carried.kind === "plan" && (
          <>
            <PlanBlock plan={carried.plan} session={session} onOpen={carried.plan && onOpenPlan ? () => onOpenPlan(carried.plan!) : undefined} />
            <textarea
              className="mq-freetext"
              rows={2}
              placeholder={tr("mirror.carried_plan_feedback_ph")}
              value={feedback}
              disabled={sending}
              onChange={(e) => setFeedback(e.target.value)}
            />
            <div className="mq-submit-row mq-footer">
              <button type="button" className="ghost" disabled={sending} onClick={() => void send({ decision: "discard" })}>
                <Icon name="close" /> {tr("mirror.carried_discard")}
              </button>
              <button
                type="button"
                className="ghost"
                disabled={sending}
                onClick={() => void send({ decision: "reject", feedback })}
              >
                {tr("mirror.carried_plan_reject")}
              </button>
              {/* ★承認は「承認」ではなく実行の指示である（docs/log/75 §75.10 E の実測: 文章で
                  承認を送ると claude は ExitPlanMode を出し直さずそのまま実行する）。
                  取り消せない決定なので confirm を挟む。 */}
              <button
                type="button"
                className="btn primary"
                disabled={sending}
                onClick={() => {
                  if (!window.confirm(tr("mirror.carried_plan_approve_confirm"))) return;
                  void send({ decision: "approve", feedback });
                }}
              >
                {tr("mirror.carried_plan_approve")}
              </button>
            </div>
          </>
        )}
        {carried.kind === "permission" && (
          <>
            {/* 許可の答えは死んだツール呼び出しには届かない。運べるのは事実だけなので、
                このカードは「何を訊かれていたか」を出して「続けて」の 1 手だけを持つ。 */}
            <div className="mt-perm-msg">{carried.permission}</div>
            <div className="mq-submit-row mq-footer">
              <button type="button" className="ghost" disabled={sending} onClick={() => void send({ decision: "discard" })}>
                <Icon name="close" /> {tr("mirror.carried_discard")}
              </button>
              <button type="button" className="btn primary" disabled={sending} onClick={() => void send({ decision: "continue" })}>
                {tr("mirror.carried_continue")}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
