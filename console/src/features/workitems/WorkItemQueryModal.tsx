// Saved queries for the work item inbox (docs/log/80 §80.16-1). Deliberately a plain list +
// one small form: af holds the query, not a copy of the tracker, so the "filter UI" IS
// this text field. GitHub search syntax goes in as-is, JQL likewise.
//
// Bitbucket is the one exception (§80.22): its query is not a dialect the member already
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

// Default query per provider. These defaults ARE the "only my open assignments" scoping
// (docs/log/80 §80.7): the initial value is where the decision not to sync everything shows.
// GitHub takes search syntax, Jira takes JQL; af only maps between them and stores each dialect
// verbatim.
//
// Bitbucket's default is the one that cannot be used as-is (docs/log/80 §80.19.1). Measured: the
// original bet that putting the words needing replacement into the default would make the error
// explain itself did not hold (§80.22) — `workspace/repo` was saved unchanged and the resulting
// 404 was read as some other error. The assembly UI appears whenever the repository list can be
// fetched, so this default is only reached by someone who dropped to free text.
const DEFAULT_QUERY: Record<string, string> = {
  github: "assignee:@me is:open",
  jira: "assignee = currentUser() AND statusCategory != Done",
  bitbucket: 'workspace/repo reviewers.uuid="@me"',
};

// Source order. Product names, so not subject to i18n (docs/log/28 §4).
const PROVIDERS = [
  { id: "github", name: "GitHub" },
  { id: "jira", name: "Jira" },
  { id: "bitbucket", name: "Bitbucket" },
];

// Bitbucket's three intents. Labels stay short (one segment line) and only the selected one gets
// a line of description: the difference between "waiting on my review" and "my own PRs" is not
// carried by the words alone, because the scope searched (repository vs. workspace) is itself
// what the answer covers.
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
  // Bitbucket assembly (docs/log/80 §80.23). `bbRepos === null` means not fetched yet; an empty
  // array means fetched but no candidates (stopped, not connected, or an error), and that falls
  // back to free text.
  // Several intents can be selected at once; they are not exclusive. Wanting both "waiting on my
  // review" and "my own PRs" is normal, and adding them one at a time would make the user pick
  // the same target twice.
  const [bbIntents, setBbIntents] = useState<BbIntent[]>(["reviewing"]);
  const [bbTarget, setBbTarget] = useState("");
  const [bbRepos, setBbRepos] = useState<string[] | null>(null);
  const [bbRaw, setBbRaw] = useState(false);

  // Switching provider replaces the default only in an untouched query field, so a half-written
  // JQL is never discarded.
  const pickProvider = (next: string) => {
    setProvider(next);
    setQuery((cur) => (cur === "" || Object.values(DEFAULT_QUERY).includes(cur) ? DEFAULT_QUERY[next] || "" : cur));
  };

  // Fetch the candidate targets from the connection only when Bitbucket is selected, and once.
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

  // The target granularity is decided by whether any selected intent needs a repository. With
  // nothing selected, offer the repository side, which is the default granularity.
  const bbNeedRepo = bbIntents.length === 0 || bbIntents.some(bbNeedsRepo);
  const bbOptions = bbRepos ? (bbNeedRepo ? bbRepos : bbWorkspaces(bbRepos)) : [];
  // Do not ask when there is only one candidate; a single workspace is the common case.
  useEffect(() => {
    if (bbOptions.length === 1 && !bbTarget) setBbTarget(bbOptions[0]);
  }, [bbOptions.length, bbTarget]); // eslint-disable-line react-hooks/exhaustive-deps

  // Adding or removing an intent never discards the target. A repository folds up to its
  // workspace, but the reverse cannot be inferred, so adding a repository-scoped intent while
  // only a workspace is selected asks the user to choose again.
  const toggleIntent = (it: BbIntent) => {
    const next = bbIntents.includes(it) ? bbIntents.filter((x) => x !== it) : [...bbIntents, it];
    setBbIntents(next);
    const needRepo = next.length === 0 || next.some(bbNeedsRepo);
    setBbTarget((cur) => (!cur ? cur : needRepo ? (cur.includes("/") ? cur : "") : bbWorkspaceOf(cur)));
  };

  // Assembly from the list is available only for Bitbucket, with candidates, and not after the
  // user dropped to free text.
  const bbBuild = provider === "bitbucket" && !bbRaw && !!bbRepos && bbRepos.length > 0;
  const effQueries = bbBuild ? bbQueries(bbIntents, bbTarget) : query.trim() ? [query.trim()] : [];

  // Only the Bitbucket assembly adds several at once; every other provider still adds one.
  // The label applies only when adding one: two rows with the same name are indistinguishable, so
  // for several the CP's default (the query string itself) is used.
  const add = async () => {
    if (!effQueries.length || busy) return;
    setBusy(true);
    try {
      const one = effQueries.length === 1;
      let failed = "";
      for (const q of effQueries) {
        const res = await workItemQueryCreate({ provider, label: one ? label.trim() : "", query: q, repoHint, enabled: true });
        // Keep going after a failure. When one of three is rejected, "two added and one refused"
        // tells the user what to do next; "nothing added" does not.
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
      // bbTarget and the selected intents are kept: adding again for the same target is a common
      // flow, and clearing the selection would force the user to re-count the list to see whether
      // anything was added at all.
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
      {/* Content must sit in ui-modal-body. ui-modal itself has no padding (the heading carries
          its own), so a child placed directly in it sticks to the frame. */}
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
        {/* Branch name template (docs/log/80 P2). The preview is there because one worked example
            conveys better than prose that {slug} is empty for a Japanese title, so the result is
            feature/issue-45. */}
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
              // i18n-exempt: the non-ASCII title sample itself; translating it destroys the example (docs/log/28 §4)
              branch2: branchForItem({ key: "PROJ-123", title: "ログイン後に一覧が空になる" }, settings.workItemBranchTemplate),
            })}
          </p>
        </div>
        <div className="wi-qform">
          {/* A segmented control (the same ui-seg as the other modals), not a select. There are
              three sources and no more, so there is nothing to collapse, and the native dropdown
              rendered its options unreadably in the dark theme (the background stayed
              transparent). */}
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
          {/* Bitbucket alone is assembled instead of being typed into a query field
              (docs/log/80 §80.23). Both the leading `<workspace>/<repo>` and
              `reviewers.uuid="@me"` are af's own inventions, not a dialect users write; the
              default `workspace/repo` was saved unreplaced and 404'd in practice (§80.23).
              GitHub search syntax and JQL are real dialects, so those keep a pass-through
              field. */}
          {bbBuild ? (
            <>
              {/* Checkboxes, not a pick-one segmented control: the three are not exclusive, and
                  wanting both "waiting on my review" and "my own PRs" is normal. */}
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
              {/* This is a Bitbucket limitation, not af's. Said in one line, and only once the
                  intent is selected, so nobody reads it as having written the query wrong. */}
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
              {/* What gets saved is one query string per row. It is shown so that a row error
                  read later can be matched against this exact string. */}
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
              {/* Only Bitbucket needs this note. GitHub and Jira can express where to look
                  outside the query, but the Bitbucket API has no cross-repository search, so the
                  target goes at the front (docs/log/80 §80.19.1). Without saying so, the user
                  learns it from a 404. */}
              {provider === "bitbucket" && <p className="wi-qhint">{tr("wi.query_bb_hint")}</p>}
              {/* Never stay silent about a failed candidate fetch (workspace stopped, not
                  connected). Unanswered, the free-text field looks like the intended design
                  rather than a fallback. */}
              {provider === "bitbucket" && bbRepos?.length === 0 && <p className="wi-qhint">{tr("wi.bb_list_failed")}</p>}
              {provider === "bitbucket" && bbRaw && !!bbRepos?.length && (
                <button type="button" className="linklike wi-qmode" onClick={() => setBbRaw(false)}>
                  {tr("wi.bb_pick_list")}
                </button>
              )}
            </>
          )}
          <label>
            {/* A Jira issue is not tied to a repository, so this is the only hint for where to
                launch — it is the project-to-working-copy mapping. */}
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
