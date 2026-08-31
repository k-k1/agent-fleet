// 作業計画パネル（docs/log/33 第5段）。
//
// 会話が持つ「圧縮を跨いで原文のまま運ばれる枠」を見る・直す・引き直すための面。
// 圧縮の引き継ぎ要約（notice カード）が LLM の要約＝毎回書き換わるのに対し、ここは
// **原文がそのまま次のセッションへ渡る**ので、利用者にとっては「アシスタントに絶対
// 忘れさせない場所」になる。
//
// 3つの更新契機（chat_plan.go 冒頭）のうち 2 と 3 の口がここ:
//   - 更新ボタン … 直近の会話から計画を引き直す（壁打ちで計画が動いた直後に押す）
//   - 編集 / クリア … 自動更新の取りこぼしと誤上書きを人が直す最後の砦
//
// 原文キャリーフォワードは「古い計画が原文のまま強く復活して新しい合意を上書きする」
// 壊れ方をしうる。編集の口がこの壊れ方に対する唯一の救済なので、パネルは畳めても
// 消せない（計画がある会話ではヘッダーのボタンが常に出る）。
import { useEffect, useState } from "react";
import { MarkdownView } from "../viewer/MarkdownView.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { chatSetPlan, chatRefreshPlan } from "./api.ts";
import { errText } from "../../core/api/client.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import type { Conversation } from "../../types/chat.ts";

interface ChatPlanProps {
  conversationId: string;
  plan: string;
  updatedAt?: number;
  /** 会話ロック待ちになる操作（ターン実行中・圧縮中・WS停止）を止める。 */
  disabled?: boolean;
  onUpdated: (conv: Conversation) => void;
  onClose: () => void;
}

export function ChatPlan({ conversationId, plan, updatedAt, disabled, onUpdated, onClose }: ChatPlanProps) {
  const tr = useT();
  const askConfirm = useConfirm();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(plan);
  const [busy, setBusy] = useState<"" | "refresh" | "save">("");
  const [error, setError] = useState("");

  // 別ペイン / 圧縮 / 自動更新で計画が動いたら、閲覧中の本文は追随させる。編集中だけは
  // 追随させない（利用者が今打っている文字を消す方が害が大きい — 保存時に後勝ちになる）。
  useEffect(() => {
    if (!editing) setDraft(plan);
  }, [plan, editing]);

  const run = async (kind: "refresh" | "save", call: () => Promise<Conversation & { error?: unknown }>) => {
    setBusy(kind);
    setError("");
    try {
      const c = await call();
      if (c && c.id) {
        onUpdated(c);
        setEditing(false);
      } else {
        setError(c?.error ? errText(c.error) : tr("chat.plan.failed"));
      }
    } catch {
      setError(tr("chat.plan.failed"));
    } finally {
      setBusy("");
    }
  };

  const doClear = async () => {
    if (
      !(await askConfirm({
        title: tr("chat.plan.clear_confirm_title"),
        body: tr("chat.plan.clear_confirm_body"),
        confirmLabel: tr("chat.plan.clear"),
        danger: true,
      }))
    )
      return;
    void run("save", () => chatSetPlan(conversationId, ""));
  };

  const working = !!busy;
  return (
    <section className="chat-plan" aria-label={tr("chat.plan.title")}>
      <header className="cp-head">
        <span className="cp-title">
          <Icon name="checklist" /> {tr("chat.plan.title")}
        </span>
        {updatedAt ? <span className="cp-ts">{fmtDateTime(updatedAt)}</span> : null}
        <span className="cp-actions">
          {editing ? (
            <>
              <button type="button" className="cp-btn" disabled={working} onClick={() => void run("save", () => chatSetPlan(conversationId, draft))}>
                <Icon name={busy === "save" ? "loading" : "check"} spin={busy === "save"} />
                {tr("common.save")}
              </button>
              <button type="button" className="cp-btn" disabled={working} onClick={() => { setEditing(false); setDraft(plan); }}>
                {tr("common.cancel")}
              </button>
            </>
          ) : (
            <>
              <button
                type="button"
                className="cp-btn"
                disabled={working || disabled}
                title={tr("chat.plan.refresh_tip")}
                onClick={() => void run("refresh", () => chatRefreshPlan(conversationId))}
              >
                <Icon name={busy === "refresh" ? "loading" : "refresh"} spin={busy === "refresh"} />
                {busy === "refresh" ? tr("chat.plan.refreshing") : tr("chat.plan.refresh")}
              </button>
              <button type="button" className="cp-btn" disabled={working || disabled} onClick={() => setEditing(true)}>
                <Icon name="edit" /> {tr("chat.plan.edit")}
              </button>
              {plan ? (
                <button type="button" className="cp-btn" disabled={working || disabled} onClick={() => void doClear()}>
                  <Icon name="trash" /> {tr("chat.plan.clear")}
                </button>
              ) : null}
            </>
          )}
          <button type="button" className="cp-btn cp-close" title={tr("common.close")} onClick={onClose}>
            <Icon name="chevron-up" />
          </button>
        </span>
      </header>
      {error && (
        <div className="chat-error" role="alert">
          {error}
        </div>
      )}
      {editing ? (
        <textarea
          className="cp-edit"
          value={draft}
          spellCheck={false}
          placeholder={tr("chat.plan.placeholder")}
          onChange={(e) => setDraft(e.target.value)}
        />
      ) : plan ? (
        <div className="cp-body">
          <MarkdownView source={plan} />
        </div>
      ) : (
        <p className="cp-empty muted">{tr("chat.plan.empty")}</p>
      )}
      <p className="cp-note muted">{tr("chat.plan.note")}</p>
    </section>
  );
}
