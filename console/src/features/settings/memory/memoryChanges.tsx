// AgentMemorySection — the member's after-the-fact view of AF-owned agent memory (ADR 0108
// decision 8). Agents publish their writes without approval, so this is where a bad memory is
// found (who, when, what) and undone. Undoing never rewrites history: the Agent writes the
// earlier state back as a new change by the member.
//
// Only the newest change of a memory offers the way back; the Agent refuses an older one
// anyway, because undoing it would silently drop what came after.

import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errDetail, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { Diff } from "../../scm/GitDiff.tsx";
import { useT, tMaybe } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { ClaudeImportPanel } from "./memoryClaudeImport.tsx";
import type { ChangeDiff, MemoryChange, SecretFinding } from "./memoryTypes.ts";

// An unknown op from a newer Agent is printed raw rather than breaking the row.
const opLabel = (op: string): string => tMaybe("mem.af_op_" + op) ?? op;

export function AgentMemorySection({ reload, onChanged }: { reload: number; onChanged: () => void }) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [changes, setChanges] = useState<MemoryChange[] | null>(null);
  // Rows the Agent left out because a value in them failed the scan; only the count is sent.
  const [withheld, setWithheld] = useState(0);
  const [loadErr, setLoadErr] = useState("");
  const [sel, setSel] = useState("");
  const [diff, setDiff] = useState<ChangeDiff | null>(null);
  const [busy, setBusy] = useState(false);
  const [mine, setMine] = useState(0);

  const load = useCallback(async (signal: AbortSignal) => {
    const res = await api("api/agents/memory/entries/changes?limit=100");
    if (signal.aborted) return true;
    if (isTransientErr(res)) return false;
    // A failed read is said, not shown as an empty history.
    setLoadErr(res?.error ? errText(res.error) : "");
    setChanges(res?.error ? [] : (res?.changes ?? []));
    setWithheld(res?.error ? 0 : (res?.withheld ?? 0));
    return true;
  }, []);
  useRetryLoad(load, [reload, mine]);

  useEffect(() => {
    if (!changes?.length) {
      setSel("");
      return;
    }
    setSel((cur) => (cur && changes.some((c) => c.commit === cur) ? cur : changes[0].commit));
  }, [changes]);

  useEffect(() => {
    if (!sel) {
      setDiff(null);
      return;
    }
    let live = true;
    setDiff(null);
    // The Agent scans this diff before it answers (the generic memory diff does not), and
    // sends masked findings instead of a diff that fails.
    api("api/agents/memory/entries/diff?commit=" + encodeURIComponent(sel))
      .then((d) => live && setDiff(d?.error ? { diff: "" } : d))
      .catch(() => live && setDiff({ diff: "" }));
    return () => {
      live = false;
    };
  }, [sel]);

  const where = (c: MemoryChange) => (c.scope === "user" ? tr("mem.af_scope_user") : (c.project?.display ?? ""));
  const who = (c: MemoryChange) =>
    c.authorKind === "member" ? tr("mem.af_by_member") : `${c.authorKind} · ${c.authorSession}`;

  // The commit also goes in the query: the CP audit ledger reads the URL only, never the body.
  const send = (c: MemoryChange, forget: boolean, ack: boolean) =>
    apiJSON("api/agents/memory/entries/revert?commit=" + encodeURIComponent(c.commit), "POST", {
      commit: c.commit,
      forget,
      ack,
    });

  const act = async (c: MemoryChange, forget: boolean) => {
    const ok = await askConfirm({
      title: forget ? tr("mem.af_forget_title") : tr("mem.af_revert_title"),
      body: <p>{tr(forget ? "mem.af_forget_body" : "mem.af_revert_body", { name: c.name, where: where(c) })}</p>,
      confirmLabel: forget ? tr("mem.af_forget") : tr("mem.af_revert"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      let res = await send(c, forget, false);
      if (res?.error?.code === "memory_secret_detected") {
        // The earlier text trips the scan now. Only the member may let it through, and only
        // for this one request (ADR 0108 decision 9).
        const findings: SecretFinding[] = res.findings ?? [];
        const pass = await askConfirm({
          title: tr("mem.af_secret_title"),
          body: (
            <>
              <p>{tr("mem.af_secret_body")}</p>
              <ul className="mem-findings">
                {findings.map((f, i) => (
                  <li key={i}>
                    <code>{f.rule}</code> {tr("mem.af_secret_line", { line: f.line })} <code>{f.hint}</code>
                  </li>
                ))}
              </ul>
            </>
          ),
          confirmLabel: tr("mem.af_secret_ack"),
          danger: true,
        });
        if (!pass) return;
        res = await send(c, forget, true);
      }
      if (res?.error) {
        toast(errDetail(res.error));
        return;
      }
      toast(forget ? tr("mem.af_forgotten", { name: c.name }) : tr("mem.af_reverted", { name: c.name }), {
        kind: "success",
      });
      setMine((n) => n + 1);
      onChanged();
    } catch {
      // The request may or may not have reached the Agent: say so and re-read the list, which
      // shows what actually happened.
      toast(tr("mem.af_failed"));
      setMine((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  const selected = changes?.find((c) => c.commit === sel) ?? null;

  return (
    <section className="mem-section">
      <div className="mem-head">
        <h3>{tr("mem.af_title")}</h3>
      </div>
      <p className="muted ds-hint">{tr("mem.af_intro")}</p>
      {loadErr && <p className="mem-warn">{loadErr}</p>}
      {withheld > 0 && <p className="mem-warn">{tr("mem.af_withheld", { n: withheld })}</p>}
      <div className="mem-body">
        <ul className="mem-list">
          {changes === null ? (
            <li className="muted pad">{tr("common.loading")}</li>
          ) : changes.length === 0 ? (
            !loadErr && withheld === 0 && <li className="muted pad">{tr("mem.af_empty")}</li>
          ) : (
            changes.map((c) => (
              <li key={c.commit}>
                <button
                  type="button"
                  className={"mem-snap" + (c.commit === sel ? " active" : "")}
                  aria-current={c.commit === sel ? "true" : undefined}
                  onClick={() => setSel(c.commit)}
                >
                  <span className="mem-snap-when">{fmtDateTime(c.at, DATETIME_FULL)}</span>
                  <span className={"mem-badge af-op-" + c.op}>{opLabel(c.op)}</span>
                  <span className="mem-af-name">{c.name}</span>
                  <span className="mem-snap-what muted">
                    {where(c)}
                    {" · "}
                    {who(c)}
                  </span>
                </button>
              </li>
            ))
          )}
        </ul>
        <div className="mem-diff">
          {selected && (
            <div className="mem-diff-head">
              <code title={selected.commit}>{selected.commit.slice(0, 9)}</code>
              {!selected.latest ? (
                <span className="muted">{tr("mem.af_not_latest")}</span>
              ) : (
                <>
                  {selected.revertible ? (
                    <button type="button" disabled={busy} onClick={() => void act(selected, false)}>
                      {tr("mem.af_revert")}
                    </button>
                  ) : (
                    <span className="muted">{tr("mem.af_not_revertible")}</span>
                  )}
                  {selected.live && (
                    <button type="button" disabled={busy} onClick={() => void act(selected, true)}>
                      {tr("mem.af_forget")}
                    </button>
                  )}
                </>
              )}
            </div>
          )}
          {selected &&
            (diff === null ? (
              <pre className="diff muted">{tr("common.loading")}</pre>
            ) : diff.withheld ? (
              <div className="pad">
                <p className="mem-warn">{tr("mem.af_diff_withheld")}</p>
                <ul className="mem-findings">
                  {(diff.findings ?? []).map((f, i) => (
                    <li key={i}>
                      <code>{f.rule}</code> {tr("mem.af_secret_line", { line: f.line })} <code>{f.hint}</code>
                    </li>
                  ))}
                </ul>
              </div>
            ) : (
              <Diff text={diff.diff} embedded wrap />
            ))}
        </div>
      </div>
      <ClaudeImportPanel reload={reload} onChanged={() => setMine((n) => n + 1)} />
    </section>
  );
}
