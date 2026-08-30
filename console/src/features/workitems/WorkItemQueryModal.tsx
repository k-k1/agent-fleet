// Saved queries for the work item inbox (docs/log/80 §80.16-1). Deliberately a plain list +
// one small form: af holds the query, not a copy of the tracker, so the "filter UI" IS
// this text field. GitHub search syntax goes in as-is, JQL likewise.
//
// ★ Bitbucket is the one exception (§80.22): its query is not a dialect the member already
// writes but a shape af invented (the `<workspace>/<repo>` prefix, `reviewers.uuid="@me"`),
// so it is assembled from the connection's own repository list instead of typed.
import { useEffect, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { useReposStore } from "../repos/store.ts";
import { setSetting, useSettings } from "../../lib/settings.ts";
import { bitbucketRepoList, workItemQueryCreate, workItemQueryDelete, workItemQueryUpdate } from "./api.ts";
import { BB_INTENTS, bbNeedsRepo, bbQueries, bbRepoNames, bbWorkspaceOf, bbWorkspaces } from "./bitbucketQuery.ts";
import type { BbIntent } from "./bitbucketQuery.ts";
import { DEFAULT_BRANCH_TEMPLATE, branchForItem } from "./read.ts";
import type { WorkItemQuery } from "./read.ts";

interface Props {
  queries: WorkItemQuery[];
  onClose(): void;
  onChanged(): void;
  onSaved(): void;
}

// provider ごとの既定クエリ。ここが「自分にアサインされた未完了だけ」という既定の
// 絞り込みそのもの（docs/log/80 §80.7）—— 全件同期をしないという設計を初期値で表している。
// GitHub は検索構文、Jira は JQL。af は写像を持つだけで、方言はそのまま保存する。
//
// ★ Bitbucket の既定だけ「そのまま使えない」（docs/log/80 §80.19.1）。⚠️ 置き換えるべき語を
// 既定値に置いておけばエラーが自分でそれを言う、という当初の目算は**実機で外れた**
// （§80.22）—— `workspace/repo` のまま保存され、404 が「別のエラー」として読まれた。
// 一覧を引ける限り組み立て UI が出るので、この既定値が使われるのは手書きに降りたときだけ。
const DEFAULT_QUERY: Record<string, string> = {
  github: "assignee:@me is:open",
  jira: "assignee = currentUser() AND statusCategory != Done",
  bitbucket: 'workspace/repo reviewers.uuid="@me"',
};

// 取得元の並び。製品名なので i18n 対象ではない（docs/log/28 §4）。
const PROVIDERS = [
  { id: "github", name: "GitHub" },
  { id: "jira", name: "Jira" },
  { id: "bitbucket", name: "Bitbucket" },
];

// Bitbucket の 3 つの意図。ラベルは短く（セグメントの 1 行）、説明は選んだ物だけ 1 行出す
// —— 「レビュー待ち」と「自分の PR」の違いは語だけでは足りず、**どこまで見るか**
// （リポジトリ / ワークスペース）が答えの範囲そのものだから。
const BB_LABEL = {
  reviewing: "wi.bb_intent_reviewing",
  repo_open: "wi.bb_intent_repo_open",
  authored: "wi.bb_intent_authored",
} as const satisfies Record<BbIntent, string>;
const BB_DESC = {
  reviewing: "wi.bb_desc_reviewing",
  repo_open: "wi.bb_desc_repo_open",
  authored: "wi.bb_desc_authored",
} as const satisfies Record<BbIntent, string>;

export function WorkItemQueryModal({ queries, onClose, onChanged, onSaved }: Props) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const repos = useReposStore((s) => s.repos);
  const settings = useSettings();
  const [provider, setProvider] = useState("github");
  const [label, setLabel] = useState("");
  const [query, setQuery] = useState(queries.length ? "" : DEFAULT_QUERY.github);
  const [repoHint, setRepoHint] = useState("");
  const [busy, setBusy] = useState(false);
  // Bitbucket の組み立て（docs/log/80 §80.23）。`bbRepos === null` は「まだ引いていない」、
  // 空配列は「引いたが候補が無い（＝停止中・未接続・エラー）」で、後者は手書きに落ちる。
  // ★ 意図は**複数選べる**（排他ではない）。「レビュー待ち」と「自分の PR」はどちらも見たい物で、
  //   1 本ずつ足させると同じ対象を 2 回選ばせることになる。
  const [bbIntents, setBbIntents] = useState<BbIntent[]>(["reviewing"]);
  const [bbTarget, setBbTarget] = useState("");
  const [bbRepos, setBbRepos] = useState<string[] | null>(null);
  const [bbRaw, setBbRaw] = useState(false);

  // provider を切り替えたら、まだ手を入れていないクエリ欄だけ既定を差し替える
  // （書きかけの JQL を勝手に消さない）。
  const pickProvider = (next: string) => {
    setProvider(next);
    setQuery((cur) => (cur === "" || Object.values(DEFAULT_QUERY).includes(cur) ? DEFAULT_QUERY[next] || "" : cur));
  };

  // Bitbucket を選んだときだけ、接続から「どこを見るか」の候補を引く。1 回だけ。
  useEffect(() => {
    if (provider !== "bitbucket" || bbRepos) return;
    let alive = true;
    bitbucketRepoList()
      .then((d) => alive && setBbRepos(bbRepoNames(d)))
      .catch(() => alive && setBbRepos([]));
    return () => {
      alive = false;
    };
  }, [provider, bbRepos]);

  // 対象の粒度は「選んだ中に**リポジトリが要る意図**が 1 つでもあるか」で決まる。
  // 1 つも選んでいないときはリポジトリ側（既定の粒度）を出しておく。
  const bbNeedRepo = bbIntents.length === 0 || bbIntents.some(bbNeedsRepo);
  const bbOptions = bbRepos ? (bbNeedRepo ? bbRepos : bbWorkspaces(bbRepos)) : [];
  // 候補が 1 つしか無いなら選ばせない（ワークスペースが 1 つ＝ほとんどの人）。
  useEffect(() => {
    if (bbOptions.length === 1 && !bbTarget) setBbTarget(bbOptions[0]);
  }, [bbOptions.length, bbTarget]); // eslint-disable-line react-hooks/exhaustive-deps

  // 意図を足し引きしても対象は捨てない。リポジトリ → ワークスペースは畳めるが、逆は決められない
  // （ワークスペースしか選んでいない状態でリポジトリが要る意図を足したら、選び直してもらう）。
  const toggleIntent = (it: BbIntent) => {
    const next = bbIntents.includes(it) ? bbIntents.filter((x) => x !== it) : [...bbIntents, it];
    setBbIntents(next);
    const needRepo = next.length === 0 || next.some(bbNeedsRepo);
    setBbTarget((cur) => (!cur ? cur : needRepo ? (cur.includes("/") ? cur : "") : bbWorkspaceOf(cur)));
  };

  // 一覧から組み立てられるのは「Bitbucket ＋ 候補があり ＋ 手書きに降りていない」ときだけ。
  const bbBuild = provider === "bitbucket" && !bbRaw && !!bbRepos && bbRepos.length > 0;
  const effQueries = bbBuild ? bbQueries(bbIntents, bbTarget) : query.trim() ? [query.trim()] : [];

  // 複数まとめて足せるのは Bitbucket の組み立てのときだけ（他は今までどおり 1 本）。
  // ★ 表示名は 1 本のときだけ効かせる —— 同じ名前の行が 2 本並んでも見分けられないので、
  //   複数のときは CP の既定（クエリ文字列そのもの）に任せる。
  const add = async () => {
    if (!effQueries.length || busy) return;
    setBusy(true);
    try {
      const one = effQueries.length === 1;
      let failed = "";
      for (const q of effQueries) {
        const res = await workItemQueryCreate({ provider, label: one ? label.trim() : "", query: q, repoHint, enabled: true });
        // ⚠️ 途中で落ちても残りは足す。3 本のうち 1 本が弾かれたときに「何も増えていない」より、
        //    「2 本増えて 1 本だけ断られた」の方が、次に何をすればよいかが分かる。
        if (res && typeof res === "object" && "error" in res && res.error) failed = errText(res.error) || t("wi.query_save_failed");
      }
      if (failed) {
        toast(failed, { kind: "warn" });
        onChanged();
        return;
      }
      setLabel("");
      setQuery("");
      setRepoHint("");
      // ★ bbTarget と選んだ意図は残す。同じ対象で足し直す流れがありふれているのと、
      //   選択が消えると「本当に足せたのか」を一覧で数え直させることになる。
      onSaved();
      onChanged();
    } catch {
      toast(t("wi.query_save_failed"), { kind: "warn" });
    } finally {
      setBusy(false);
    }
  };

  const toggle = async (q: WorkItemQuery) => {
    try {
      await workItemQueryUpdate(q.id, { ...q, enabled: !q.enabled });
      onChanged();
    } catch {
      toast(t("wi.query_save_failed"), { kind: "warn" });
    }
  };

  const remove = async (q: WorkItemQuery) => {
    const ok = await askConfirm({
      title: t("wi.query_delete_title"),
      body: t("wi.query_delete_confirm", { label: q.label }),
      confirmLabel: t("common.delete"),
      danger: true,
    });
    if (!ok) return;
    try {
      const r = await workItemQueryDelete(q.id);
      if (!r.ok) {
        toast(t("common.delete_failed"), { kind: "warn" });
        return;
      }
      onChanged();
    } catch {
      toast(t("common.delete_failed"), { kind: "warn" });
    }
  };

  return (
    <Modal title={tr("wi.queries")} onClose={onClose} className="wi-qmodal">
      {/* ★ 中身は ui-modal-body に載せる。ui-modal 自身に padding は無く（見出しが
          自分で持つ形）、直に子を置くと本文だけが枠に貼りつく。 */}
      <div className="ui-modal-body">
        <p className="wi-qhelp">{tr("wi.queries_help")}</p>
        {queries.length > 0 && (
          <ul className="wi-qlist">
            {queries.map((q) => (
              <li className={"wi-qrow" + (q.enabled ? "" : " off")} key={q.id}>
                <div className="wi-qinfo">
                  <div className="wi-qlabel">
                    <span className="wi-qprov">{q.provider}</span>
                    {q.label}
                  </div>
                  <code className="wi-qquery">{q.query}</code>
                  {q.repoHint && <span className="wi-qhint">{tr("wi.repo_hint_is", { repo: q.repoHint })}</span>}
                  {q.lastError && <div className="wi-qerr">{q.lastError}</div>}
                </div>
                <IconButton icon={q.enabled ? "eye" : "eye-closed"} label={tr(q.enabled ? "wi.query_disable" : "wi.query_enable")} onClick={() => void toggle(q)} />
                <IconButton icon="trash" label={tr("common.delete")} onClick={() => void remove(q)} />
              </li>
            ))}
          </ul>
        )}
        {/* ブランチ名テンプレート（docs/log/80 P2）。プレビューを添えるのは、{slug} が
            日本語タイトルで空になる（＝結果が feature/issue-45 になる）ことが、
            説明文よりも 1 行の実例で伝わるから。 */}
        <div className="wi-qbranch">
          <label>
            <span>{tr("wi.branch_template")}</span>
            <input
              value={settings.workItemBranchTemplate}
              placeholder={DEFAULT_BRANCH_TEMPLATE}
              spellCheck={false}
              onChange={(e) => setSetting("workItemBranchTemplate", e.target.value)}
            />
          </label>
          <p className="wi-qhint">
            {tr("wi.branch_preview", {
              branch: branchForItem({ key: "acme/web#45", title: "Fix the empty list" }, settings.workItemBranchTemplate),
              // i18n-exempt: 非 ASCII のタイトル見本そのもの（訳すと例が例でなくなる・docs/log/28 §4）
              branch2: branchForItem({ key: "PROJ-123", title: "ログイン後に一覧が空になる" }, settings.workItemBranchTemplate),
            })}
          </p>
        </div>
        <div className="wi-qform">
          {/* ★ select ではなくセグメント（他のモーダルと同じ ui-seg）。取得元は 3 つで
              増えないので畳む理由が無く、ネイティブのドロップダウンは暗色テーマで
              選択肢が読めなかった（背景が透明のまま描かれる）。 */}
          <div className="wi-qfield">
            <span>{tr("wi.query_provider")}</span>
            <div className="ui-seg" role="group" aria-label={tr("wi.query_provider")}>
              {PROVIDERS.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  className={"seg-btn" + (provider === p.id ? " active" : "")}
                  aria-pressed={provider === p.id}
                  onClick={() => pickProvider(p.id)}
                >
                  {p.name}
                </button>
              ))}
            </div>
          </div>
          <label>
            <span>{tr("wi.query_label")}</span>
            <input
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder={tr("wi.query_label_ph")}
              disabled={effQueries.length > 1}
            />
          </label>
          {effQueries.length > 1 && <p className="wi-qhint">{tr("wi.bb_label_auto")}</p>}
          {/* ★ Bitbucket だけ、クエリ欄そのものを出さずに組み立てる（docs/log/80 §80.23）。
              先頭の `<workspace>/<repo>` も `reviewers.uuid="@me"` も **af の発明**で、
              利用者が普段書いている方言ではない —— 既定値の `workspace/repo` を
              置き換えないまま保存され 404 になった、が実際に起きた（§80.23）。
              GitHub の検索構文と JQL は本物の方言なので、これまでどおり素通しの入力欄。 */}
          {bbBuild ? (
            <>
              {/* ★ セグメント（1 つ選ぶ）ではなくチェック。3 つは排他ではなく、
                  「レビュー待ち」と「自分の PR」はどちらも見たい物だから。 */}
              <div className="wi-qfield">
                <span>{tr("wi.bb_intent")}</span>
                <div className="wi-qchecks" role="group" aria-label={tr("wi.bb_intent")}>
                  {BB_INTENTS.map((it) => (
                    <label key={it} className="ssm-check">
                      <input type="checkbox" checked={bbIntents.includes(it)} onChange={() => toggleIntent(it)} />
                      <span className="wi-qcheck-name">{tr(BB_LABEL[it])}</span>
                      <span className="wi-qcheck-desc">{tr(BB_DESC[it])}</span>
                    </label>
                  ))}
                </div>
              </div>
              {/* ⚠️ これは af の制限ではなく Bitbucket の制限。選んだ人が「書き方を間違えた」と
                  読まないよう、選んだときだけ 1 行で断る。 */}
              {bbIntents.includes("authored") && <p className="wi-qhint">{tr("wi.bb_authored_note")}</p>}
              <label>
                <span>{tr(bbNeedRepo ? "wi.bb_target_repo" : "wi.bb_target_ws")}</span>
                <select value={bbTarget} onChange={(e) => setBbTarget(e.target.value)}>
                  <option value="">{tr("wi.bb_target_none")}</option>
                  {bbOptions.map((o) => (
                    <option key={o} value={o}>
                      {o}
                    </option>
                  ))}
                </select>
              </label>
              {/* 保存されるのは 1 本 1 行のクエリ文字列（列は増えていない）。出しておくのは
                  ★ 後で行のエラーを読むときに、この文字列と突き合わせられるようにするため。 */}
              {effQueries.length > 0 && (
                <div className="wi-qfield">
                  <span>{tr("wi.bb_preview")}</span>
                  {effQueries.map((q) => (
                    <code className="wi-qquery" key={q}>
                      {q}
                    </code>
                  ))}
                </div>
              )}
              <button type="button" className="linklike wi-qmode" onClick={() => setBbRaw(true)}>
                {tr("wi.bb_write_own")}
              </button>
            </>
          ) : (
            <>
              <label>
                <span>{tr("wi.query_expr")}</span>
                <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={DEFAULT_QUERY[provider]} spellCheck={false} />
              </label>
              {/* ★ Bitbucket にだけ説明が要る。GitHub と Jira は「どこを見るか」をクエリの
                  外に置けるが、Bitbucket の API は横断検索を持たないので先頭に対象を書く
                  （docs/log/80 §80.19.1）。ここを書かないと、利用者は 404 を見てから学ぶことになる。 */}
              {provider === "bitbucket" && <p className="wi-qhint">{tr("wi.query_bb_hint")}</p>}
              {/* 候補を引けなかった（Workspace 停止中・未接続）ことは黙らない。「一覧から
                  選べるはずでは？」を先に答えておかないと、手書き欄が仕様に見える。 */}
              {provider === "bitbucket" && bbRepos?.length === 0 && <p className="wi-qhint">{tr("wi.bb_list_failed")}</p>}
              {provider === "bitbucket" && bbRaw && !!bbRepos?.length && (
                <button type="button" className="linklike wi-qmode" onClick={() => setBbRaw(false)}>
                  {tr("wi.bb_pick_list")}
                </button>
              )}
            </>
          )}
          <label>
            {/* Jira は課題がリポジトリに紐づかないので、起動先はここが唯一の手がかりに
                なる（プロジェクト → 作業コピーの対応表がこれ）。 */}
            <span>{provider === "jira" ? tr("wi.query_repo_hint_jira") : tr("wi.query_repo_hint")}</span>
            <select value={repoHint} onChange={(e) => setRepoHint(e.target.value)}>
              <option value="">{tr("wi.query_repo_any")}</option>
              {repos
                .filter((r) => !r.worktree)
                .map((r) => (
                  <option key={r.name} value={r.name}>
                    {r.name}
                  </option>
                ))}
            </select>
          </label>
          <Button onClick={() => void add()} disabled={!effQueries.length || busy}>
            {tr("wi.add_query")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
