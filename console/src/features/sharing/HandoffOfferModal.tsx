// HandoffOfferModal — 所有者が「この続きを共有先の誰かに」差し出す面（docs/77 / ADR 0057）。
//
// 宛先候補は**共有 ACL の逆引き**で、テナント名簿は引かない。だから ShareCreateModal のような
// 検索コンボボックスではなく、素の選択肢になる（既に「この人に見せる」と決めた相手しか出ない）。
//
// push ゲートは CP が所有者 Agent に聞いた事実で、ここでは表示するだけ。判定をこちらで組み立て
// 直すと、送信時のサーバ判定と条件が 2 か所に分かれて必ずずれる（docs/77 §77.5）。
import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { ShareCreateModal } from "./ShareCreateModal.tsx";
import { useHandoffStore } from "./handoffStore.ts";
import "./sharing.css";

interface RecipientCandidate {
  userKey: string;
  email: string;
}

/** CP がそのまま中継する Agent の判定（workspace/agent/session_handoff_context.go）。 */
interface HandoffContext {
  repo?: string;
  vcs?: string;
  branch?: string;
  remote?: string;
  headSha?: string;
  ahead?: number;
  dirty?: boolean;
  blocked?: string;
  warning?: string;
}

/** ゲートの理由は機械トークンで来る。文言はここで解決する（サーバに日本語を持たせない）。 */
const BLOCKED_KEYS = {
  unpushed_commits: "handoff.blocked_unpushed",
  no_upstream: "handoff.blocked_no_upstream",
  detached_head: "handoff.blocked_detached",
} as const;

function blockedKey(reason: string): (typeof BLOCKED_KEYS)[keyof typeof BLOCKED_KEYS] | "handoff.blocked_unknown" {
  return BLOCKED_KEYS[reason as keyof typeof BLOCKED_KEYS] ?? "handoff.blocked_unknown";
}

export function HandoffOfferModal({
  session,
  initialTitle,
  initialPrompt,
  onClose,
  onSent,
}: {
  session: string;
  initialTitle?: string;
  initialPrompt: string;
  onClose: () => void;
  onSent?: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [loading, setLoading] = useState(true);
  const [members, setMembers] = useState<RecipientCandidate[]>([]);
  const [ctx, setCtx] = useState<HandoffContext | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [recipient, setRecipient] = useState("");
  const [title, setTitle] = useState(initialTitle || "");
  const [prompt, setPrompt] = useState(initialPrompt);
  const [ackWarning, setAckWarning] = useState(false);
  const [sending, setSending] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);

  const load = () => {
    setLoading(true);
    api(`api/sessions/${encodeURIComponent(session)}/handoff-recipients`)
      .then((d) => {
        if (d?.error) {
          setLoadErr(errText(d.error));
          setMembers([]);
          return;
        }
        setLoadErr("");
        setMembers(Array.isArray(d.members) ? d.members : []);
        setCtx((d.context || null) as HandoffContext | null);
        setRecipient((prev) => prev || (d.members?.[0]?.userKey ?? ""));
      })
      .catch(() => setLoadErr(tr("handoff.load_failed")))
      .finally(() => setLoading(false));
  };
  useEffect(load, [session]); // eslint-disable-line react-hooks/exhaustive-deps

  const blocked = ctx?.blocked || "";
  const warning = ctx?.warning || "";
  const canSubmit = !!recipient && !!title.trim() && !!prompt.trim() && !blocked && (!warning || ackWarning);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit || sending) return;
    setSending(true);
    const d = await apiJSON("api/session-handoff-offers", "POST", {
      sessionName: session,
      recipientUserKey: recipient,
      title: title.trim(),
      prompt,
      ackWarning,
    }).catch(() => ({ error: { message: tr("handoff.send_failed") } }));
    setSending(false);
    if (d?.error) {
      toast(errText(d.error));
      // ゲートに当たった／既に未処理がある、はここで初めて分かることがある（開いてから
      // commit した等）。最新の判定を取り直して画面を合わせる。
      load();
      return;
    }
    void useHandoffStore.getState().refresh();
    onSent?.();
    onClose();
  };

  return (
    <>
      <Modal
        title={tr("handoff.offer_title")}
        onClose={onClose}
        as="form"
        onSubmit={submit}
        lockClose={sending}
        className="share-create-modal"
      >
        <div className="ui-modal-body">
          <p className="ui-field-hint">{tr("handoff.offer_intro")}</p>
          {loading ? (
            <p className="ui-field-hint">{tr("common.loading")}</p>
          ) : loadErr ? (
            <p className="ui-field-hint handoff-blocked">{loadErr}</p>
          ) : members.length === 0 ? (
            <>
              {/* 共有していない相手には渡せない（ADR 0057 決定 2）。ここで行き止まりに
                  しないよう、共有を張る導線をその場に置く。 */}
              <p className="ui-field-hint handoff-blocked">{tr("handoff.not_shared")}</p>
              <Button variant="ghost" onClick={() => setShareOpen(true)}>
                <Icon name="broadcast" /> {tr("handoff.share_first")}
              </Button>
            </>
          ) : (
            <>
              <label className="ui-field">
                <span className="ui-field-label">{tr("handoff.recipient")}</span>
                <select value={recipient} onChange={(e) => setRecipient(e.target.value)}>
                  {members.map((m) => (
                    <option key={m.userKey} value={m.userKey}>
                      {m.email || m.userKey}
                    </option>
                  ))}
                </select>
              </label>
              {/* 座標。B のディスクに所有者の未コミット変更は無いので、どの commit を
                  引き継ぐのかを送る側にも見せる。 */}
              {ctx?.branch && (
                <p className="ui-field-hint">
                  {tr("handoff.coordinates", {
                    branch: ctx.branch,
                    sha: (ctx.headSha || "").slice(0, 8),
                    remote: ctx.remote || "-",
                  })}
                </p>
              )}
              {blocked && <p className="ui-field-hint handoff-blocked">{tr(blockedKey(blocked))}</p>}
              {!blocked && warning && (
                <label className="ui-field-inline">
                  <input type="checkbox" checked={ackWarning} onChange={(e) => setAckWarning(e.target.checked)} />
                  {tr("handoff.ack_dirty")}
                </label>
              )}
              <label className="ui-field">
                <span className="ui-field-label">{tr("handoff.offer_title_label")}</span>
                <input value={title} maxLength={512} onChange={(e) => setTitle(e.target.value)} />
              </label>
              <label className="ui-field">
                <span className="ui-field-label">{tr("handoff.offer_prompt_label")}</span>
                <textarea
                  className="handoff-prompt-edit"
                  value={prompt}
                  spellCheck={false}
                  onChange={(e) => setPrompt(e.target.value)}
                />
              </label>
              {/* 送信後の本文は凍結される（ADR 0057 決定 7）。黙って効かないのが一番悪いので言う。 */}
              <p className="ui-field-hint">{tr("handoff.frozen_hint")}</p>
            </>
          )}
        </div>
        <div className="ui-modal-foot">
          <Button variant="ghost" onClick={onClose}>
            {tr("common.cancel")}
          </Button>
          <Button type="submit" variant="primary" disabled={!canSubmit || sending}>
            {tr("handoff.send")}
          </Button>
        </div>
      </Modal>
      {shareOpen && (
        <ShareCreateModal
          initialTarget={`session:${session}`}
          onClose={() => setShareOpen(false)}
          onCreated={() => {
            setShareOpen(false);
            load();
          }}
        />
      )}
    </>
  );
}
