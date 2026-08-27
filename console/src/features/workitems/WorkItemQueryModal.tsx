// Saved queries for the work item inbox (docs/80 §80.16-1). Deliberately a plain list +
// one small form: af holds the query, not a copy of the tracker, so the "filter UI" IS
// this text field. GitHub search syntax goes in as-is (JQL in P1).
import { useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { useReposStore } from "../repos/store.ts";
import { setSetting, useSettings } from "../../lib/settings.ts";
import { workItemQueryCreate, workItemQueryDelete, workItemQueryUpdate } from "./api.ts";
import { DEFAULT_BRANCH_TEMPLATE, branchForItem } from "./read.ts";
import type { WorkItemQuery } from "./read.ts";

interface Props {
  queries: WorkItemQuery[];
  onClose(): void;
  onChanged(): void;
  onSaved(): void;
}

// provider ごとの既定クエリ。ここが「自分にアサインされた未完了だけ」という既定の
// 絞り込みそのもの（docs/80 §80.7）—— 全件同期をしないという設計を初期値で表している。
// GitHub は検索構文、Jira は JQL。af は写像を持つだけで、方言はそのまま保存する。
const DEFAULT_QUERY: Record<string, string> = {
  github: "assignee:@me is:open",
  jira: "assignee = currentUser() AND statusCategory != Done",
};

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

  // provider を切り替えたら、まだ手を入れていないクエリ欄だけ既定を差し替える
  // （書きかけの JQL を勝手に消さない）。
  const pickProvider = (next: string) => {
    setProvider(next);
    setQuery((cur) => (cur === "" || Object.values(DEFAULT_QUERY).includes(cur) ? DEFAULT_QUERY[next] || "" : cur));
  };

  const add = async () => {
    if (!query.trim() || busy) return;
    setBusy(true);
    try {
      const res = await workItemQueryCreate({ provider, label: label.trim(), query: query.trim(), repoHint, enabled: true });
      if (res && typeof res === "object" && "error" in res && res.error) {
        toast(errText(res.error) || t("wi.query_save_failed"), { kind: "warn" });
        return;
      }
      setLabel("");
      setQuery("");
      setRepoHint("");
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
      {/* ブランチ名テンプレート（docs/80 P2）。プレビューを添えるのは、{slug} が
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
            // i18n-exempt: 非 ASCII のタイトル見本そのもの（訳すと例が例でなくなる・docs/28 §4）
            branch2: branchForItem({ key: "PROJ-123", title: "ログイン後に一覧が空になる" }, settings.workItemBranchTemplate),
          })}
        </p>
      </div>
      <div className="wi-qform">
        <label>
          <span>{tr("wi.query_provider")}</span>
          <select value={provider} onChange={(e) => pickProvider(e.target.value)}>
            <option value="github">GitHub</option>
            <option value="jira">Jira</option>
          </select>
        </label>
        <label>
          <span>{tr("wi.query_label")}</span>
          <input value={label} onChange={(e) => setLabel(e.target.value)} placeholder={tr("wi.query_label_ph")} />
        </label>
        <label>
          <span>{tr("wi.query_expr")}</span>
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={DEFAULT_QUERY[provider]} spellCheck={false} />
        </label>
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
        <Button onClick={() => void add()} disabled={!query.trim() || busy}>
          {tr("wi.add_query")}
        </Button>
      </div>
    </Modal>
  );
}
