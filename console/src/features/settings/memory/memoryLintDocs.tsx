// DocTargets — the document-target part of the lint report (ADR 0130 decision 11). A memory
// superseded by a repository document keeps pointing at it; the Agent checks each target on every
// report and lists the ones whose file moved or went, whose heading went or whose merge was
// reverted. The member repairs one at a time: re-point it (a rename's new place in one click, or
// any path / heading / commit by hand), or restore the memory into agents' view. Each repair
// quotes the revision the report showed, so a memory that moved meanwhile is refused, and each is
// a change the member can revert in the change list.

import { useState } from "react";
import { errDetail } from "../../../core/api/client.ts";
import { isOk } from "../api.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import {
  retargetDocument,
  restoreDocument,
  type DocRepair,
} from "./memoryApi.ts";
import type { LintDocFinding, LintDocReport } from "./memoryTypes.ts";

const short = (c?: string) => (c ?? "").slice(0, 12);

function Problem({ f }: { f: LintDocFinding }) {
  const tr = useT();
  return (
    <>
      {tr(("mem.lint_doc_p_" + f.problem) as MsgKey, {
        path: f.path ?? "",
        commit: short(f.commit),
      })}
    </>
  );
}

function Row({
  f,
  project,
  head,
  busy,
  repair,
}: {
  f: LintDocFinding;
  project: string;
  /** The default branch's commit the report read: where a hand re-point is checked by default. */
  head: string;
  busy: boolean;
  repair: (verb: "retarget" | "restore", body: DocRepair) => void;
}) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const [path, setPath] = useState(f.path ?? f.target.path);
  const [heading, setHeading] = useState(f.target.heading ?? "");
  // The revision the report read holds what it reported (a renamed file with the heading); the
  // renaming commit is evidence only and may lack the heading.
  const [commit, setCommit] = useState(head);
  const base = { project, name: f.memory, revision: f.revision };
  const where = f.target.heading
    ? `${f.target.path} # ${f.target.heading}`
    : f.target.path;
  return (
    <li>
      <code>{f.memory}</code> → <code>{where}</code>{" "}
      <span className="muted">@{short(f.target.commit)}</span> —{" "}
      <Problem f={f} />{" "}
      {/* Only when the API can take it as is: a re-point needs a heading, and a target stored
          without one gets it through the form. */}
      {f.problem === "renamed" &&
        f.headingFound &&
        f.path &&
        f.target.heading &&
        head && (
          <>
            <Button
              disabled={busy}
              onClick={() =>
                repair("retarget", {
                  ...base,
                  path: f.path,
                  heading: f.target.heading ?? "",
                  commit: head,
                })
              }
            >
              {tr("mem.lint_doc_follow", { path: f.path })}
            </Button>{" "}
          </>
        )}
      {/* Offered for another repository too: the re-point is checked against this project's. */}
      <Button
        disabled={busy}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {tr("mem.lint_doc_repoint")}
      </Button>{" "}
      <Button disabled={busy} onClick={() => repair("restore", base)}>
        {tr("mem.lint_doc_restore")}
      </Button>
      {open && (
        <form
          className="mem-doc-repoint"
          onSubmit={(e) => {
            e.preventDefault();
            repair("retarget", {
              ...base,
              path: path.trim(),
              heading: heading.trim(),
              commit: commit.trim(),
            });
          }}
        >
          <label>
            <span className="muted">{tr("mem.lint_doc_path")}</span>
            <input value={path} onChange={(e) => setPath(e.target.value)} />
          </label>
          <label>
            <span className="muted">{tr("mem.lint_doc_heading")}</span>
            <input
              value={heading}
              onChange={(e) => setHeading(e.target.value)}
            />
          </label>
          <label>
            <span className="muted">{tr("mem.lint_doc_commit")}</span>
            <input value={commit} onChange={(e) => setCommit(e.target.value)} />
          </label>
          <Button
            type="submit"
            disabled={busy || !path.trim() || !heading.trim() || !commit.trim()}
          >
            {tr("mem.lint_doc_apply")}
          </Button>
        </form>
      )}
    </li>
  );
}

export function DocTargets({
  docs,
  project,
  head,
  onRepaired,
}: {
  docs: LintDocReport;
  project: string;
  head: string;
  onRepaired: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const repair = async (verb: "retarget" | "restore", body: DocRepair) => {
    setBusy(true);
    try {
      const res = await (verb === "retarget"
        ? retargetDocument(body)
        : restoreDocument(body));
      if (!isOk(res)) {
        toast(errDetail(res.error), { kind: "error" });
        return;
      }
      toast(
        tr(
          verb === "retarget"
            ? "mem.lint_doc_retargeted"
            : "mem.lint_doc_restored",
        ),
        { kind: "success" },
      );
      onRepaired();
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mem-lint-docs">
      <h5>{tr("mem.lint_doc_title")}</h5>
      <p className="muted ds-hint">{tr("mem.lint_doc_hint")}</p>
      <p>
        {tr("mem.lint_doc_checked", {
          n: docs.checked,
          f: docs.findings.length,
        })}
      </p>
      {docs.findings.length > 0 && (
        <ul className="mem-lint-list">
          {docs.findings.map((f) => (
            <Row
              key={`${f.memory}:${f.revision}`}
              f={f}
              project={project}
              head={head}
              busy={busy}
              repair={(v, b) => void repair(v, b)}
            />
          ))}
        </ul>
      )}
    </div>
  );
}
