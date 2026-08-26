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
import { workItemQueryCreate, workItemQueryDelete, workItemQueryUpdate } from "./api.ts";
import type { WorkItemQuery } from "./read.ts";

interface Props {
  queries: WorkItemQuery[];
  onClose(): void;
  onChanged(): void;
  onSaved(): void;
}

// 既定の 1 本。ここが「自分にアサインされた未完了だけ」という既定の絞り込みそのもの
// （docs/80 §80.7）— 全件同期をしないという設計を、初期値の側で表している。
const DEFAULT_QUERY = "assignee:@me is:open";

export function WorkItemQueryModal({ queries, onClose, onChanged, onSaved }: Props) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const repos = useReposStore((s) => s.repos);
  const [label, setLabel] = useState("");
  const [query, setQuery] = useState(queries.length ? "" : DEFAULT_QUERY);
  const [repoHint, setRepoHint] = useState("");
  const [busy, setBusy] = useState(false);

  const add = async () => {
    if (!query.trim() || busy) return;
    setBusy(true);
    try {
      const res = await workItemQueryCreate({ provider: "github", label: label.trim(), query: query.trim(), repoHint, enabled: true });
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
                <div className="wi-qlabel">{q.label}</div>
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
      <div className="wi-qform">
        <label>
          <span>{tr("wi.query_label")}</span>
          <input value={label} onChange={(e) => setLabel(e.target.value)} placeholder={tr("wi.query_label_ph")} />
        </label>
        <label>
          <span>{tr("wi.query_expr")}</span>
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={DEFAULT_QUERY} spellCheck={false} />
        </label>
        <label>
          <span>{tr("wi.query_repo_hint")}</span>
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
