// HandoffInboxModal — 受け取る側の面（docs/log/77 / ADR 0057）。
//
// 「1 ボタン」は**押すまでに考えることが無い**という意味であって、押した瞬間に起動する
// ことではない（docs/log/77 §77.1）。押すと前埋めされた起動モーダルが開き、本文も作業コピーも
// エージェントも受け手が選ぶ —— 起きるのは受け手の Workspace で、費用も受け手に付くのだから
// この確認は省けない。
//
// 起動そのものは既存の起動導線（useLaunchTarget → LaunchModal → useStartWork）に丸ごと乗せる。
// CP が他人の Workspace を操作しないのがこの機能の骨格（ADR 0057 決定 3）なので、受諾は
// 「起動できた」の事後申告として StartHost から送る。
import { useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useLaunchSeed, useLaunchTarget, useReposStore } from "../repos/store.ts";
import { useHandoffStore, type HandoffOffer } from "./handoffStore.ts";
import "./sharing.css";

/** remote URL から作業コピー名の当たりを付ける（`…/k-k1/agent-fleet.git` → `agent-fleet`）。
 *  受け手の作業コピー一覧には remote の**ホスト**しか無いので、URL 同士の突合はできない。
 *  ここは既定値を埋めるためだけの推測で、最終判断は受け手がセレクトで行う。 */
export function repoNameFromRemote(remote?: string): string {
  if (!remote) return "";
  const last = remote.replace(/\/+$/, "").split("/").filter(Boolean).at(-1) || "";
  return last.replace(/\.git$/i, "");
}

function OfferRow({ offer, onDone }: { offer: HandoffOffer; onDone: () => void }) {
  const tr = useT();
  const toast = useToast();
  const repos = useReposStore((s) => s.repos);
  const guess = useMemo(() => repoNameFromRemote(offer.repoRemote), [offer.repoRemote]);
  const [repoName, setRepoName] = useState(() => (repos.some((r) => r.name === guess) ? guess : repos[0]?.name || ""));
  const [busy, setBusy] = useState(false);

  const accept = () => {
    const repo = repos.find((r) => r.name === repoName);
    if (!repo?.path) {
      toast(tr("handoff.accept_no_repo"));
      return;
    }
    // どの offer を受けたのかを起動導線へ持たせる。起動が成功した時点で StartHost が
    // accept を送る（キャンセルされた起動で受諾済みにしてはいけない）。
    useLaunchSeed.getState().set(offer.prompt || "", offer.title, "", "", offer.id);
    useLaunchTarget.getState().open({ name: repo.name, path: repo.path, branch: offer.branch || repo.branch, worktree: repo.worktree });
    onDone();
  };
  const decline = async () => {
    if (busy) return;
    setBusy(true);
    const d = await apiJSON(`api/session-handoff-offers/${encodeURIComponent(offer.id)}/decline`, "POST", {});
    setBusy(false);
    if (d?.error) {
      toast(errText(d.error));
    }
    void useHandoffStore.getState().refresh();
  };

  return (
    <li className="handoff-inbox-row">
      <header className="handoff-inbox-head">
        <strong>{offer.title}</strong>
        <span className="muted">{tr("handoff.from", { who: offer.ownerUserKey || "" })}</span>
      </header>
      <p className="ui-field-hint">
        {tr("handoff.coordinates", {
          branch: offer.branch || "-",
          sha: (offer.headSha || "").slice(0, 8),
          remote: offer.repoRemote || "-",
        })}
      </p>
      {/* 本文は畳まずに出す。受け手はこれを読んだうえで押す、が 1 ボタンの前提。 */}
      <pre className="mirror-handoff-prompt">{offer.prompt}</pre>
      <label className="ui-field">
        <span className="ui-field-label">{tr("handoff.accept_repo")}</span>
        <select value={repoName} onChange={(e) => setRepoName(e.target.value)}>
          {repos.map((r) => (
            <option key={r.name} value={r.name}>
              {r.name}
            </option>
          ))}
        </select>
      </label>
      <p className="ui-field-hint">{tr("handoff.accept_cost_hint")}</p>
      <div className="handoff-inbox-actions">
        <Button variant="ghost" disabled={busy} onClick={() => void decline()}>
          {tr("handoff.decline")}
        </Button>
        <Button variant="primary" disabled={busy || !repoName} onClick={accept}>
          <Icon name="run" /> {tr("handoff.accept")}
        </Button>
      </div>
    </li>
  );
}

export function HandoffInboxModal({ onClose }: { onClose: () => void }) {
  const tr = useT();
  const received = useHandoffStore((s) => s.received);
  return (
    <Modal title={tr("handoff.inbox_title")} onClose={onClose}>
      <div className="ui-modal-body">
        {received.length === 0 ? (
          <p className="ui-field-hint">{tr("handoff.inbox_empty")}</p>
        ) : (
          <ul className="handoff-inbox">
            {received.map((o) => (
              <OfferRow key={o.id} offer={o} onDone={onClose} />
            ))}
          </ul>
        )}
      </div>
    </Modal>
  );
}
