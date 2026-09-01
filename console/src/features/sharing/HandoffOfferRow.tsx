// HandoffOfferRow — 受け取った引き継ぎ 1 件の面（docs/log/77 / ADR 0057）。
//
// 受信箱（HandoffInboxModal）と共有ビューの帯（SharedSessionView）の両方から出す。切り出して
// あるのは、**受諾の導線が 1 か所しか無かった**のが実利用で最初に踏まれた穴だから: 通知を
// 押した先は共有ビューで、そこには受け取る口が無く、唯一の口はレール見出しのアイコンだった。
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

export function HandoffOfferRow({ offer, onDone }: { offer: HandoffOffer; onDone: () => void }) {
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
      {/* 作業コピーが 1 つも無いと受諾ボタンは押せない。理由を言わないと「受け取るボタンが
          効かない」としか見えないので、次の一手（自分で clone する）まで書く。 */}
      {repos.length === 0 && <p className="ui-field-hint handoff-blocked">{tr("handoff.accept_no_repo_hint")}</p>}
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
