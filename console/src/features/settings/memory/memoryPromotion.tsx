// PromotionPanel — from a member's memory to the project's documents (ADR 0130 decision 11). One
// project at a time: the Agent lists the memories that proved themselves (confirmed by several
// sessions on their current revision, quiet for some days, anchors on the default branch), and
// for the one the member picks builds the proposed change — document, section, text, the patch —
// and runs the secret scan and the repository's own pre-commit hook on it in a throwaway clone.
//
// Nothing here commits, pushes or opens a pull request: the member copies the patch and opens it
// (or asks a session to). Once it is merged, the member names the merge commit and the memory is
// superseded by the document, quoting the revision and text digest the proposal showed, so a
// memory edited while the pull request waited is refused rather than hidden.

import { useEffect, useRef, useState } from "react";
import {
  errDetail,
  errText,
  isTransientErr,
} from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { copyText } from "../../../lib/clipboard.ts";
import { isOk } from "../api.ts";
import { useApiOnce } from "../parts/useApiLoad.ts";
import { useEscLayer } from "../../../lib/escLayer.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import {
  getMemoryProjects,
  getPromoteCandidates,
  proposePromotion,
  supersedeByDocument,
} from "./memoryApi.ts";
import type {
  ProjectRow,
  PromoteCandidates,
  PromoteProposal,
} from "./memoryTypes.ts";

function Proposal({
  p,
  project,
  onSuperseded,
}: {
  p: PromoteProposal;
  project: string;
  onSuperseded: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [commit, setCommit] = useState("");
  const [busy, setBusy] = useState(false);
  const src = p.sources[0];
  const pc = p.scans.preCommit;
  const hook = pc.changed
    ? tr("mem.promo_hook_changed")
    : pc.passed
      ? tr("mem.promo_hook_passed")
      : pc.absent
        ? tr("mem.promo_hook_absent")
        : pc.ran
          ? tr("mem.promo_hook_failed")
          : tr("mem.promo_hook_not_run", { note: pc.note ?? "" });

  const copy = async () => {
    const ok = await copyText(p.patch ?? "");
    toast(tr(ok ? "mem.promo_copied" : "mem.promo_copy_failed"), {
      kind: ok ? "success" : "error",
    });
  };
  const supersede = async () => {
    if (!src) return;
    setBusy(true);
    try {
      const res = await supersedeByDocument({
        project,
        name: src.name,
        revision: src.revision,
        path: p.target.path,
        heading: p.target.heading,
        commit: commit.trim(),
        bodySha256: src.bodySha256,
      });
      if (!isOk(res)) {
        toast(errDetail(res.error), { kind: "error" });
        return;
      }
      toast(tr("mem.promo_superseded", { path: p.target.path }), {
        kind: "success",
      });
      onSuperseded();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mem-promo-proposal" aria-label={src?.name}>
      <p>
        {tr("mem.promo_target", {
          path: p.target.path,
          heading: p.target.heading,
        })}{" "}
        {!p.fileExists && (
          <span className="mem-badge">{tr("mem.promo_new_file")}</span>
        )}{" "}
        {p.fileExists && !p.sectionExists && (
          <span className="mem-badge">{tr("mem.promo_new_section")}</span>
        )}
      </p>
      <p className={p.ready ? "" : "mem-warn"}>
        {tr(p.ready ? "mem.promo_ready" : "mem.promo_not_ready")}
      </p>
      {p.scans.secret.length > 0 && (
        <p className="mem-warn">
          {tr("mem.promo_secret", { n: p.scans.secret.length })}
        </p>
      )}
      <p className={pc.passed || pc.absent ? "muted" : "mem-warn"}>{hook}</p>
      {pc.output && <pre className="mem-promo-pre">{pc.output}</pre>}
      {p.warnings
        .filter((w) => w !== "new-file")
        .map((w) => (
          <p key={w} className="mem-warn">
            {tr(("mem.promo_warn_" + w) as MsgKey)}
          </p>
        ))}
      {p.text !== undefined && (
        <details open>
          <summary>{tr("mem.promo_text")}</summary>
          <pre className="mem-promo-pre">{p.text}</pre>
        </details>
      )}
      {p.patch && (
        <details>
          <summary>{tr("mem.promo_patch")}</summary>
          <pre className="mem-promo-pre">{p.patch}</pre>
        </details>
      )}
      {p.patch && (
        <div className="mem-ci-actions">
          <Button onClick={() => void copy()}>{tr("mem.promo_copy")}</Button>
        </div>
      )}
      {/* Offered whatever the scans said: they gate the pull request, and the supersede checks the
          merged document itself. A proposal built after the merge is often "not ready" only
          because the section is already there. Not when the secret scan withheld the target. */}
      {src && p.target.path && p.target.heading && (
        <form
          className="mem-promo-merged"
          onSubmit={(e) => {
            e.preventDefault();
            void supersede();
          }}
        >
          <h5>{tr("mem.promo_merged_title")}</h5>
          <p className="muted ds-hint">
            {tr("mem.promo_merged_hint", { rev: src.revision })}
          </p>
          <label>
            <span className="muted">{tr("mem.promo_commit")}</span>{" "}
            <input value={commit} onChange={(e) => setCommit(e.target.value)} />
          </label>{" "}
          <Button
            type="submit"
            disabled={busy || !/^[0-9a-fA-F]{7,64}$/.test(commit.trim())}
          >
            {tr("mem.promo_supersede")}
          </Button>
        </form>
      )}
    </div>
  );
}

export function PromotionPanel({
  reload,
  onClose,
}: {
  reload: number;
  onClose?: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  useEscLayer(() => onClose?.(), !!onClose);
  const [rows, setRows] = useState<ProjectRow[] | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [project, setProject] = useState("");
  const [list, setList] = useState<PromoteCandidates | null>(null);
  const [listErr, setListErr] = useState("");
  const [again, setAgain] = useState(0);
  // The proposal shown, for which project and memory; one is built at a time (each runs a hook).
  // It stays bound to the project it was asked for: a memory of the same name in the project
  // chosen since must never be shown it, nor superseded by it.
  const [proposal, setProposal] = useState<{
    project: string;
    name: string;
    p: PromoteProposal;
  } | null>(null);
  const [proposing, setProposing] = useState("");
  const current = useRef(project);
  useEffect(() => {
    current.current = project;
  }, [project]);

  useRetryLoad(
    async (signal) => {
      const res = await getMemoryProjects();
      if (signal.aborted) return true;
      if (isTransientErr(res)) return false;
      setLoadErr(isOk(res) ? "" : errText(res.error));
      const ps = isOk(res)
        ? (res.projects ?? []).filter((r) => r.count > 0)
        : [];
      setRows(ps);
      setProject((cur) => cur || ps[0]?.project.id || "");
      return true;
    },
    [reload],
  );

  useApiOnce(
    project ? () => getPromoteCandidates(project) : null,
    (res) => {
      if (!isOk(res)) {
        setListErr(errDetail(res.error));
        setList(null);
        return;
      }
      setListErr("");
      setList({
        minSessions: 0,
        quietDays: 0,
        candidates: [],
        held: [],
        ...res,
      });
    },
    () => setListErr(tr("mem.af_failed")),
    [project, again, tr],
  );

  const propose = async (name: string) => {
    const at = project;
    setProposing(name);
    setProposal(null);
    try {
      const res = await proposePromotion({ project: at, name });
      if (current.current !== at) return; // another project was chosen meanwhile
      if (!isOk(res)) {
        toast(errDetail(res.error), { kind: "error" });
        return;
      }
      setProposal({ project: at, name, p: res });
    } finally {
      setProposing("");
    }
  };

  return (
    <section className="mem-wiz" aria-label={tr("mem.promo_title")}>
      <h4>{tr("mem.promo_title")}</h4>
      <p className="muted ds-hint">{tr("mem.promo_intro")}</p>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      {rows !== null && rows.length === 0 && !loadErr && (
        <p className="muted">{tr("mem.promo_no_projects")}</p>
      )}
      {rows !== null && rows.length > 0 && (
        <label className="mem-scope">
          <span className="muted">{tr("mem.promo_project")}</span>
          <select
            value={project}
            onChange={(e) => {
              setProject(e.target.value);
              setList(null);
              setProposal(null);
            }}
          >
            {rows.map((r) => (
              <option key={r.project.id} value={r.project.id}>
                {r.project.display} ({r.count})
              </option>
            ))}
          </select>{" "}
          <Button onClick={() => setAgain((n) => n + 1)}>
            {tr("mem.lint_rerun")}
          </Button>
        </label>
      )}
      {listErr && <p className="mem-warn">{listErr}</p>}
      {project && !list && !listErr && (
        <p className="muted">{tr("common.loading")}</p>
      )}
      {list && (
        <div className="mem-promo-list" aria-live="polite">
          <p className="muted ds-hint">
            {tr("mem.promo_rule", { n: list.minSessions, d: list.quietDays })}
            {list.ref && (
              <>
                {" "}
                {tr("mem.promo_ref", {
                  ref: list.ref,
                  commit: list.commit ?? "",
                })}
              </>
            )}
          </p>
          {list.skipped?.map((s) => (
            <p key={s} className="muted ds-hint">
              {tr("mem.lint_skipped", { what: s })}
            </p>
          ))}
          {list.candidates.length === 0 && (
            <p className="muted">{tr("mem.promo_empty")}</p>
          )}
          <ul className="mem-lint-list">
            {list.candidates.map((c) => (
              <li key={c.name}>
                <code>{c.name}</code>{" "}
                <span className="muted">
                  ({c.kind}, r{c.revision})
                </span>{" "}
                <span className="muted">
                  {tr("mem.promo_sessions", { n: c.sessions })}
                </span>{" "}
                <Button
                  disabled={!!proposing}
                  onClick={() => void propose(c.name)}
                >
                  {tr("mem.promo_propose")}
                </Button>
                {proposing === c.name && (
                  <p className="muted">{tr("mem.promo_proposing")}</p>
                )}
                {proposal?.project === project && proposal.name === c.name && (
                  <Proposal
                    p={proposal.p}
                    project={proposal.project}
                    onSuperseded={() => {
                      // The answer may come after another project or proposal was opened: only
                      // the proposal it was for is closed, and only its project's list re-read.
                      const done = proposal;
                      setProposal((cur) => (cur === done ? null : cur));
                      if (current.current === done.project)
                        setAgain((n) => n + 1);
                    }}
                  />
                )}
              </li>
            ))}
          </ul>
          {list.held.length > 0 && (
            <details className="mem-ci-group">
              <summary>{tr("mem.promo_held", { n: list.held.length })}</summary>
              <ul className="mem-lint-list">
                {list.held.map((h) => (
                  <li key={h.name}>
                    <code>{h.name}</code>{" "}
                    <span className="muted">
                      ({h.kind}, r{h.revision})
                    </span>{" "}
                    —{" "}
                    <span className="muted">
                      {h.missing
                        .map((m) =>
                          m.detail ? `${m.rule}: ${m.detail}` : m.rule,
                        )
                        .join("; ")}
                    </span>
                  </li>
                ))}
              </ul>
            </details>
          )}
        </div>
      )}
      {onClose && (
        <div className="mem-ci-actions mem-wiz-foot">
          <Button icon="arrow-left" onClick={onClose}>
            {tr("mem.wz_back")}
          </Button>
        </div>
      )}
    </section>
  );
}
