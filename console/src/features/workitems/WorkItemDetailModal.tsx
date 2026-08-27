// 作業項目の詳細 (docs/80 §80.8 / §80.20).
//
// レールの行をクリックすると開く 1 枚。★ 行から「始める」ボタンを外したのは、41 行の
// 右端に同じボタンが 41 個並ぶと、レールが「押すと何かが起きる面」に見えて怖いから
// （利用者からの指摘・§80.20）。行は情報に戻し、操作はここに集める。
//
// 出すのは **CP が既に持っている項目だけ**（key/title/state/url/assignee/labels/repo/更新）。
// ★ 本文（説明）は出さない —— CP は本文を預からない決まりで（ADR 0061 決定 2）、ここで
// 取りに行くと「一覧を見るために Workspace を起こす」を詳細の側から作り直すことになる。
// 本文はセッションの中で `gh` / MCP が読む（§80.9）。
//
// 起動先の選択（リポジトリ・新しい worktree / 既存の作業コピー）はこの中に統合した。
// A ticket does not know where its work happens: a GitHub item names a repository but
// not which local copy, and a Jira issue names neither.
//
// ★ Why this is not the はじめる hub: that hub deliberately lists BASE clones only
// ("worktrees are task copies, launched from their tree rows"), and continuing a ticket
// in the worktree it already has is the normal second visit. It is also why this is not
// left to the launch dialog's 場所 section, which offers 新しい worktree か このコピーで
// 直接 but cannot point at a DIFFERENT existing copy.
//
// The answer is handed to the existing LaunchModal (via useLaunchTarget) rather than
// re-implementing any of it — agent, model, prompt, branch and worktree creation all
// stay in one place.
import { useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import type { Repo } from "../repos/store.ts";
import {
  canComment,
  fullLocal,
  relTime,
  stateLabel,
  stateTone,
  type WorkItem,
  type WorkItemSessionRef,
} from "./read.ts";

/** Sentinel for 「新しい作業コピー（worktree）を作る」 — not a folder name, so it cannot
 * collide with one. */
export const NEW_WORKTREE = " new-worktree";

interface Props {
  item: WorkItem;
  repos: Repo[];
  /** Working copy resolved from the query's 既定 / the item's own repo ("" = none). */
  defaultRepo: string;
  /** Ledger rows for this item — 「同じチケットに 2 人目」を起動の前に見せる。 */
  started: WorkItemSessionRef[];
  onClose(): void;
  /** target = the working copy to launch in; inPlace = the user picked an existing copy
   * (so the launch dialog must not re-default to "new worktree"). */
  onPick(target: Repo, inPlace: boolean): void;
  /** 作業コピーが 1 つも無いとき —— clone 導線を持つ はじめる ハブに委ねる。 */
  onStartHub(): void;
  onOpenSession(name: string): void;
  onReport(): void;
}

export function WorkItemDetailModal({
  item,
  repos,
  defaultRepo,
  started,
  onClose,
  onPick,
  onStartHub,
  onOpenSession,
  onReport,
}: Props) {
  const tr = useT();
  const bases = useMemo(() => repos.filter((r) => !r.worktree), [repos]);
  // 既定のリポジトリ: クエリの指定 → 項目の repo → 先頭。worktree が既定に入っていたら
  // その親を選ぶ（起動先の選択はこのあとの行で行う）。
  const initialBase = useMemo(() => {
    const hit = repos.find((r) => r.name === defaultRepo);
    if (hit) return hit.worktree ? hit.parent || "" : hit.name;
    return bases[0]?.name || "";
  }, [repos, bases, defaultRepo]);
  const [base, setBase] = useState(initialBase);
  const [where, setWhere] = useState<string>(NEW_WORKTREE);

  const baseRepo = repos.find((r) => r.name === base);
  // その base の worktree を作成順に。SVN 作業コピーには worktree の概念が無い。
  const worktrees = useMemo(
    () => repos.filter((r) => r.worktree && r.parent === base).sort((a, b) => (a.createdAt || "").localeCompare(b.createdAt || "")),
    [repos, base],
  );
  const canWorktree = !!baseRepo && baseRepo.vcs !== "svn";

  const go = () => {
    // 作業コピーがまだ 1 つも無いなら、ここで選ばせるものが無い —— clone から案内する
    // はじめる ハブに渡す（種は呼び手が置く）。
    if (bases.length === 0) {
      onStartHub();
      return;
    }
    if (!baseRepo) return;
    if (where === NEW_WORKTREE && canWorktree) {
      onPick(baseRepo, false);
      return;
    }
    const chosen = repos.find((r) => r.name === where) || baseRepo;
    onPick(chosen, true);
  };

  const rel = relTime(item.updatedAt);

  return (
    // ★ タイトルは短縮しない。レールの行は repo を落として `#312` だけを出すが、
    // 41 行のうちどれを開いたかを言えるのは**完全なキー**である（`#312` は 3 リポジトリ
    // 分ありうる）。
    <Modal title={tr("wi.detail_title", { key: item.key })} onClose={onClose} className="wi-dmodal">
      {/* ★ 中身は ui-modal-body / ui-modal-foot に載せる。ui-modal 自身に padding は
          無く（見出しと footer が自分で持つ形）、直に子を置くと本文だけが枠に貼りつく。 */}
      <div className="ui-modal-body">
        <div className="wi-dhead">
          <span className={`wi-dot tone-${stateTone(item.state)}`} title={stateLabel(item.state)}>
            <Icon name={item.kind === "pr" ? "git-pull-request" : "issues"} />
          </span>
          {/* ★ ここだけは省略しない。レールの行が 1 行で切っているものを読みに来る面なので、
              折り返してでも全文を出す。 */}
          <h3 className="wi-dtitle">{item.title}</h3>
        </div>

        {/* CP が預かっている項目そのまま。無い値の行は描かない（「—」を並べても
            読む物が増えるだけ）。 */}
        <dl className="wi-dfacts">
          <dt>{tr("wi.detail_state")}</dt>
          <dd>{stateLabel(item.state)}</dd>
          <dt>{tr("wi.detail_kind")}</dt>
          <dd>{item.kind === "pr" ? tr("wi.kind_pr") : tr("wi.kind_issue")}</dd>
          <dt>{tr("wi.detail_provider")}</dt>
          <dd>
            <span className="wi-dprov">{item.provider}</span>
          </dd>
          {item.assignee && (
            <>
              <dt>{tr("wi.detail_assignee")}</dt>
              <dd>@{item.assignee}</dd>
            </>
          )}
          {item.repo && (
            <>
              <dt>{tr("wi.detail_repo")}</dt>
              <dd>{item.repo}</dd>
            </>
          )}
          {item.labels.length > 0 && (
            <>
              <dt>{tr("wi.detail_labels")}</dt>
              <dd className="wi-dlabels">
                {item.labels.map((l) => (
                  <span className="wi-label" key={l}>
                    {l}
                  </span>
                ))}
              </dd>
            </>
          )}
          {item.updatedAt && (
            <>
              <dt>{tr("wi.detail_updated")}</dt>
              <dd title={fullLocal(item.updatedAt)}>
                {rel ? tr("wi.detail_updated_rel", { at: fullLocal(item.updatedAt), rel }) : fullLocal(item.updatedAt)}
              </dd>
            </>
          )}
        </dl>

        {/* 本文をここに出さない代わりに、「読みに行く先」は必ず出す（§80.9 と同じ理由）。 */}
        <a className="wi-dlink" href={item.url} target="_blank" rel="noreferrer noopener">
          <Icon name="link-external" />
          {tr("wi.open_external")}
        </a>

        {/* 着手済み。台帳の一番の実利がこれ —— 同じ課題に 2 人目が入るのを、起動する前に
            止める（docs/80 §80.8）。 */}
        {started.length > 0 && (
          <section className="wi-dstarted">
            <h4>{tr("wi.detail_started")}</h4>
            <ul>
              {started.map((s) => (
                <li key={s.id}>
                  <button type="button" className="wi-dsession" onClick={() => onOpenSession(s.sessionName)}>
                    <Icon name="circle-filled" />
                    {s.sessionName}
                    {s.branch ? <span className="wi-dbranch">{s.branch}</span> : null}
                  </button>
                </li>
              ))}
            </ul>
            {/* 書き戻し。押しても投稿はされない —— 下書きを読むモーダルが開くだけで、
                投稿はその中の 1 手（ADR 0061 決定 6）。 */}
            {canComment(item) && (
              <Button variant="ghost" onClick={onReport}>
                <Icon name="comment" />
                {tr("wi.report_title")}
              </Button>
            )}
          </section>
        )}

        <section className="wi-dstart">
          <h4>{tr("wi.detail_start_head")}</h4>
          {bases.length === 0 ? (
            <p className="wi-shint">{tr("wi.start_no_repos")}</p>
          ) : (
            <>
              <label className="wi-sfield">
                <span>{tr("wi.start_repo")}</span>
                <select
                  value={base}
                  onChange={(e) => {
                    setBase(e.target.value);
                    setWhere(NEW_WORKTREE); // リポジトリを変えたら起動先は選び直し
                  }}
                >
                  {bases.map((r) => (
                    <option key={r.name} value={r.name}>
                      {r.name}
                    </option>
                  ))}
                </select>
              </label>
              <label className="wi-sfield">
                <span>{tr("wi.start_where")}</span>
                <select value={where} onChange={(e) => setWhere(e.target.value)}>
                  {canWorktree && <option value={NEW_WORKTREE}>{tr("wi.start_new_worktree")}</option>}
                  <option value={base}>{tr("wi.start_in_base", { repo: base })}</option>
                  {worktrees.map((r) => (
                    <option key={r.name} value={r.name}>
                      {r.branch ? tr("wi.start_in_worktree", { branch: r.branch }) : r.name}
                    </option>
                  ))}
                </select>
              </label>
              <p className="wi-shint">
                {where === NEW_WORKTREE ? tr("wi.start_new_worktree_hint") : tr("wi.start_existing_hint")}
              </p>
            </>
          )}
        </section>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("common.cancel")}
        </Button>
        <Button onClick={go} disabled={bases.length > 0 && !baseRepo}>
          {tr("wi.start")}
        </Button>
      </footer>
    </Modal>
  );
}
