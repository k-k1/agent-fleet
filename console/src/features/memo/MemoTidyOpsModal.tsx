// The manual Tidy for a selection inside one auto-run category (ADR 0137 section 8): the assistant
// proposes merge, split, move and keep, the member reads each as a before and after, and
// approving sends the one apply call. The proposal comes from POST /api/chat/ask, so nothing
// here lets a model change a note: the Control Plane applies what the member approved, re-checks
// every rule, and records one run that can be undone. Notes that are not in an auto-run category
// keep the in-place tidy (MemoTidyModal).
import { useEffect, useMemo, useRef, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { askAssistant } from "../chat/api.ts";
import { memoQueueList, memoTidyApply } from "./api.ts";
import type { Memo, MemoCategory } from "../../types/memo.ts";
import {
  LEFT_OUT_WORDS,
  REFUSAL_WORDS,
  applyBody,
  buildOpsPrompt,
  mixesStates,
  parseOpsReply,
  readTidyDestinationIds,
  reviewOps,
  tidyDestinations,
  type PreviewItem,
  type Review,
  type TidyDest,
} from "./tidyOps.ts";

interface Props {
  memos: Memo[];
  category: MemoCategory;
  categories: MemoCategory[];
  /** Every note the section holds: the destinations' open references are read from them. */
  allMemos?: Memo[];
  onDone: () => void; // applied: refetch and close
  onReload: () => void; // refetch, stay open (the notes changed under the preview)
  onClose: () => void;
}

interface Applied {
  merged: number;
  split: number;
  moved: number;
  held: number;
}

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);

/** What the Control Plane answered to the apply, as numbers; null when it is not a run. */
function readApplied(res: unknown): Applied | null {
  if (!isObj(res) || !isObj(res.run)) return null;
  const n = (v: unknown) => (typeof v === "number" ? v : 0);
  return { merged: n(res.run.merged), split: n(res.run.split), moved: n(res.run.moved), held: n(res.run.held) };
}

export function MemoTidyOpsModal({ memos, category, categories, allMemos, onDone, onReload, onClose }: Props) {
  const tr = useT();
  const [phase, setPhase] = useState<"loading" | "preview" | "failed" | "done">("loading");
  const [err, setErr] = useState("");
  const [review, setReview] = useState<Review | null>(null);
  const [malformed, setMalformed] = useState(0);
  const [pick, setPick] = useState<Record<number, boolean>>({});
  const [busy, setBusy] = useState(false);
  const [applied, setApplied] = useState<Applied | null>(null);
  // After a refusal the preview stays readable, with the reason above it.
  const [notice, setNotice] = useState("");

  const dests = useRef<TidyDest[]>([]);
  const mixed = useMemo(() => mixesStates(memos), [memos]);

  const destName = (d: TidyDest) => `${d.repo || tr("memo.common")}/${d.category}`;
  const fromName = destName({ repo: category.repo, category: category.name } as TidyDest);

  useEffect(() => {
    let alive = true;
    (async () => {
      let listed: string[] = [];
      try {
        listed = readTidyDestinationIds(await memoQueueList(), category.id);
      } catch {
        // No listed destinations: a move then lands as approval-pending, which the Control Plane decides anyway.
      }
      dests.current = tidyDestinations(category, listed, categories);
      const r = await askAssistant(buildOpsPrompt(memos, dests.current));
      if (!alive) return;
      if (r.error) {
        setErr(errText(r.error) || tr("sx.tidy_failed"));
        setPhase("failed");
        return;
      }
      const parsed = parseOpsReply(r.reply || "");
      if (!parsed) {
        setErr(tr("memo.tidyop_parse_failed"));
        setPhase("failed");
        return;
      }
      const rv = reviewOps(parsed.ops, memos, dests.current, allMemos);
      setMalformed(parsed.malformed);
      setReview(rv);
      setPick(Object.fromEntries(rv.items.map((_, i) => [i, true])));
      setPhase("preview");
    })().catch(() => {
      if (!alive) return;
      setErr(tr("sx.tidy_failed"));
      setPhase("failed");
    });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const chosen = review ? review.items.filter((_, i) => pick[i]) : [];
  // Notes no ticked proposal changes, so that unticking a proposal moves its notes into this count.
  const changedIds = new Set(chosen.flatMap((c) => (c.kind === "merge" ? c.notes.map((n) => n.id) : [c.note.id])));
  const keptCount = memos.filter((m) => !changedIds.has(m.id)).length;

  const apply = async () => {
    if (!review || chosen.length === 0) return;
    setBusy(true);
    setNotice("");
    try {
      const res = await memoTidyApply(applyBody(category, memos, chosen.map((c) => c.op)));
      const error = isObj(res) && isObj(res.error) ? res.error : null;
      if (error) {
        if (error.code === "memo_tidy_stale") {
          setPhase("failed");
          setErr(tr("memo.tidyop_stale"));
          onReload();
        } else if (error.code === "memo_tidy_invalid") {
          const code = typeof error.tidyCode === "string" ? error.tidyCode : "";
          const words = REFUSAL_WORDS[code];
          setNotice(
            tr("memo.tidyop_invalid", {
              reason: words ? tr(words) : tr("memo.tidyop_invalid_unknown", { code }),
            }),
          );
        } else {
          setNotice(errText(res.error as Parameters<typeof errText>[0]) || tr("sx.tidy_apply_failed"));
        }
        return;
      }
      setApplied(readApplied(res) ?? { merged: 0, split: 0, moved: 0, held: 0 });
      setPhase("done");
      onReload();
    } catch {
      setNotice(tr("sx.tidy_apply_failed"));
    } finally {
      setBusy(false);
    }
  };

  const noteText = (m: Memo) => (m.kind === "file" ? `${tr("sx.target_file")} ${m.refPath}${m.body ? " — " + m.body : ""}` : m.body);
  const result = (e: "run" | "approve") => tr(e === "run" ? "memo.tidyop_result_run" : "memo.tidyop_result_approve");
  const moveEffect = (it: Extract<PreviewItem, { kind: "move" }>) =>
    it.effect === "run"
      ? tr("memo.tidyop_move_run")
      : it.effect === "approve"
        ? tr("memo.tidyop_move_approve", { from: fromName })
        : tr("memo.tidyop_move_note");

  const row = (it: PreviewItem, i: number) => (
    <label key={i} className="memo-tidy-row" data-op={it.kind}>
      <input
        type="checkbox"
        aria-label={tr("memo.tidyop_pick")}
        checked={!!pick[i]}
        onChange={(e) => setPick((p) => ({ ...p, [i]: e.target.checked }))}
      />
      <div className="memo-tidy-diff">
        <div className="memo-tidy-op">
          {it.kind === "merge" && tr("memo.tidyop_merge", { count: it.notes.length })}
          {it.kind === "split" && tr("memo.tidyop_split", { count: it.op.texts.length })}
          {it.kind === "move" && tr("memo.tidyop_move", { dest: destName(it.dest) })}
        </div>
        {(it.kind === "merge" ? it.notes : [it.note]).map((m) => (
          <div key={m.id} className="memo-tidy-before">
            {noteText(m)}
          </div>
        ))}
        {it.kind === "merge" && <div className="memo-tidy-after">{it.op.text.trim()}</div>}
        {it.kind === "split" &&
          it.op.texts.map((t, k) => (
            <div key={k} className="memo-tidy-after">
              {t.trim()}
            </div>
          ))}
        <div className="memo-tidy-effect">{it.kind === "move" ? moveEffect(it) : result(it.effect as "run" | "approve")}</div>
      </div>
    </label>
  );

  return (
    <Modal title={tr("memo.tidyop_title")} onClose={onClose} className="memo-tidy-modal" lockClose={busy}>
      <div className="ui-modal-body">
        {phase === "loading" && <div className="ui-field-hint">{tr("sx.tidy_loading")}</div>}
        {phase === "failed" && <div className="ui-field-hint warn">⚠ {err}</div>}
        {phase === "done" && applied && (
          <div className="memo-tidy-done">
            <p>
              {applied.merged + applied.split + applied.moved === 0
                ? tr("memo.tidyop_done_kept")
                : tr("memo.tidyop_done", { merged: applied.merged, split: applied.split, moved: applied.moved })}
            </p>
            {applied.held > 0 && <p>{tr("memo.tidyop_done_held", { held: applied.held })}</p>}
            <p className="ui-field-hint">{tr("memo.tidyop_done_undo")}</p>
          </div>
        )}
        {phase === "preview" && review && (
          <>
            <div className="ui-field-hint">{tr("memo.tidyop_intro")}</div>
            {mixed && <div className="ui-field-hint warn">⚠ {tr("memo.tidyop_mixed_states")}</div>}
            {notice && <div className="ui-field-hint warn">⚠ {notice}</div>}
            {malformed > 0 && <div className="ui-field-hint">{tr("memo.tidyop_malformed", { count: malformed })}</div>}
            {review.items.length === 0 && review.leftOut.length === 0 && <div className="ui-field-hint">{tr("memo.tidyop_none")}</div>}
            <div className="memo-tidy-list">{review.items.map(row)}</div>
            {review.items.length > 0 && (
              <div className="ui-field-hint">{tr("memo.tidyop_kept", { count: keptCount })}</div>
            )}
            {review.leftOut.length > 0 && (
              <div className="memo-tidy-leftout">
                <strong>{tr("memo.tidyop_out_title")}</strong>
                <div className="ui-field-hint">{tr("memo.tidyop_out_hint")}</div>
                {review.leftOut.map((l, i) => (
                  <div key={i} className="memo-tidy-row" data-left-out={l.why}>
                    <div className="memo-tidy-diff">
                      <div className="memo-tidy-op">{tr(LEFT_OUT_WORDS[l.why] as MsgKey)}</div>
                      {l.notes.map((m) => (
                        <div key={m.id} className="memo-tidy-before">
                          {noteText(m)}
                        </div>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </div>
      <footer className="ui-modal-foot">
        {phase === "done" ? (
          <Button variant="primary" onClick={onDone}>
            {tr("common.close")}
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              {tr("sx.cancel")}
            </Button>
            <Button variant="primary" disabled={phase !== "preview" || busy || chosen.length === 0} onClick={() => void apply()}>
              {chosen.length > 0 ? tr("memo.tidyop_apply", { count: chosen.length }) : tr("sx.apply")}
            </Button>
          </>
        )}
      </footer>
    </Modal>
  );
}
