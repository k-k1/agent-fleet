// useStartWork — the "作業を始める" submit path (POST /api/sessions with worktree /
// branch-conflict handling, pasted-image upload, first-prompt seeding, refreshes,
// pane open), extracted from RepoRowConnected so the per-repo LaunchModal and the
// はじめる hub (StartModal / LaunchHost, 起動導線 Ph2) share one implementation.
// `dir: ""` launches in the home directory (repo-less session) — worktree options
// and repo-scoped bookkeeping (repoLast / prompt history) are skipped there.
import { apiJSON, errText, pasteImage } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";
import { buildImagePrompt } from "../../lib/pastedImages.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { agentOf } from "../../agents/registry.ts";
import { writeRepoLast, writeRepoSubdir } from "../../lib/repoLast.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { pushPromptHistory } from "../../lib/promptHistory.ts";
import { setLaunchSeed } from "../../lib/launchSeed.ts";
import { useReposStore } from "./store.ts";
import { useFilesStore } from "../files/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";

export interface StartTarget {
  /** Working-copy path ("" = home / repo-less). */
  dir: string;
  /** Working-copy folder name for repoLast / prompt history ("" = skip). */
  repo: string;
}

export function useStartWork(): (target: StartTarget, opts: LaunchOpts) => Promise<LaunchResult> {
  const toast = useToast();
  const refreshRepos = useReposStore((s) => s.refresh);
  const refreshSessions = useSessionsStore((s) => s.refresh);

  return async ({ dir, repo }, { kind, driver, model, effort, startMode, skipPermissions, prompt, title, images, worktree, subdir, base, newBranch, useExisting }) => {
    const hasModel = agentOf(kind).caps.model;
    // 添付があるときは、保存パスを本文へ織り込むために「セッションができてから」でないと
    // 最初の指示の本文が確定しない（アップロード先がそのセッション）。それ以外は作成要求に
    // 載せて Agent に配達させる。
    const withImages = !!(images?.length && agentOf(kind).caps.imagePaste);
    const body: Record<string, unknown> = { dir, kind };
    if (driver) body.driver = driver;
    if (hasModel && model) body.model = model;
    if (effort) body.effort = effort;
    if (startMode) body.mode = startMode;
    // 権限確認（docs/76）。**既定と同じときは送らない**: 未指定なら Agent が kind 毎の
    // 既定（ui-prefs）で解決するので、設定を変えたあとに立てたセッションにも新しい既定が
    // 効く。ここで毎回値を焼き込むと、その kind の既定を後から変えても効かなくなる。
    if (typeof skipPermissions === "boolean" && agentOf(kind).caps.permissionChoice) {
      body.skip_permissions = skipPermissions;
    }
		if (title) body.title = title;
    // 作業ディレクトリ（Meta.Subdir）: the Agent resolves it INSIDE whatever working
    // copy the launch lands in — including a worktree it creates in this same call —
    // and rejects a path that isn't there, so no client-side existence check.
    if (subdir) body.subdir = subdir;
    // 最初の指示は Agent に配達させる（deliverInitialPrompt / managed は driver.Send）。
    // CLI が composer を描くまで待ってから打ち、貼り付け窓が閉じたあとに Enter を押し直し、
    // 配達を検証する — そして何より、**Console を見ていなくても走る**。以前はチャットの
    // ミラーがマウントされている間しか送られず、起動直後に別タブへ移ると「そのタブを
    // 開き直した瞬間に送信される」ように見えていた（裏のタブのペインは描画されない）。
    if (prompt && !withImages) body.initial_prompt = prompt;
    if (worktree) {
      body.worktree = true;
      body.branch = base;
      body.new_branch = newBranch;
      if (useExisting) body.use_existing = true;
    }
    let res = await apiJSON("api/sessions", "POST", body);
    // フリート再ビルドのラグ: P1.5 世代の Agent は driver:"managed" を明示拒否する
    // （driver_unsupported）。managed で立てられないなら tui で立てて動く方が親切 —
    // 1 回だけ落として再送し、その旨をトーストする。
    if (res?.error && driver === "managed" && (res.error as { code?: string }).code === "driver_unsupported") {
      toast(t("rp.managed_unsupported"));
      delete body.driver;
      if (!agentOf(kind).caps.tuiEffort) delete body.effort;
      if (!agentOf(kind).caps.tuiStartMode) delete body.mode;
      res = await apiJSON("api/sessions", "POST", body);
    }
    if (res && res.error) {
      const code = typeof res.error === "object" ? res.error.code : "";
      if (code === "branch_exists") return { ok: false, conflict: "local" as const };
      if (code === "branch_exists_remote") return { ok: false, conflict: "remote" as const };
      // The branch is checked out in another working copy (git allows only one). The
      // payload names it so the dialog can say WHERE instead of just failing.
      if (code === "branch_in_use")
        return { ok: false, conflict: "in_use" as const, worktree: String(res.error.worktree || "") };
      toast(
        worktree
          ? t("rp.worktree_launch_failed", { err: errText(res.error) })
          : t("rp.launch_failed", { err: errText(res.error) }),
      );
      return { ok: false };
    }
    if (repo) {
      writeRepoLast(repo, kind, hasModel ? model : undefined, effort, startMode);
      writeRepoSubdir(repo, subdir || "");
    }
    // repo なし（home）セッションはグループ継承が効かないので、選択中グループへ
    // 直接自動所属させる（docs/52 §1）。repo 内launchは repo 側の所属を継承する。
    if (!dir) autoAddToActiveWorkingSet("sessions", res.name);
    const chat = agentOf(kind).caps.chat;
    // Now that the session exists, upload any pasted images to it and fold their
    // saved paths into the first prompt (claude opens them with its Read tool).
    let seed = prompt;
    if (withImages) {
      const paths: string[] = [];
      for (const f of images ?? []) {
        try {
          const up = await pasteImage(res.name, f);
          if (up.status < 300 && up.path) paths.push(up.path);
          else toast(t("rp.image_upload_failed", { err: up.error ? errText(up.error) : "" }));
        } catch {
          toast(t("rp.image_upload_failed_network"));
        }
      }
      seed = buildImagePrompt(prompt, paths, kind);
      // 作成要求には載せられなかったぶんを、同じ配達（起動待ち＋二度目 Enter＋配達確認）へ
      // 渡す。拒否は握りつぶさない — 黙って消えると「送ったのに始まらない」になる。
      const del = await apiJSON(`api/sessions/${encodeURIComponent(res.name)}/input`, "POST", {
        prompt: seed,
        when_ready: true,
      }).catch(() => ({ error: { message: t("err.network") } }));
      if (del?.error) toast(t("rp.first_prompt_failed", { err: errText(del.error) }));
    }
    if (seed) {
      // 送信そのものは Agent 側で走っている。ミラーには「送った文面」を楽観エコーとして
      // 見せるだけ（launchSeed は表示用の受け渡し）— 起動から最初のターンが転写に載るまでの
      // 数秒、チャットが空のままにならないように。
      if (chat) setLaunchSeed(res.name, seed);
      if (prompt && repo) pushPromptHistory(repo, prompt);
    }
    if (worktree) {
      void refreshRepos();
      useFilesStore.getState().bump();
    }
    void refreshSessions();
    (chat ? openSessionChat : openSessionTerminal)(res.name);
    // 作られたセッション名を返すのは、引き継ぎの受諾（docs/77）が「どのセッションで
    // 受けたか」を所有者へ返すため。呼び出し側は ok だけ見ていてもよい。
    return { ok: true, name: res.name };
  };
}
