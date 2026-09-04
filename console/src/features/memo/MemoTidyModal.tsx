// MemoTidyModal (docs/log/21 tidy) — hands the selected memos to a stateless assistant
// turn that returns cleaned text + a suggested category per memo, previews old→new, and on
// approval PATCHes the changes. We never auto-apply — a bad tidy shouldn't need undo.
import { useEffect, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { askAssistant } from "../chat/api.ts";
import { memoUpdate } from "./api.ts";
import type { Memo } from "../../types/memo.ts";

interface TidyRow {
  id: string;
  cleaned: string;
  suggestedCategory: string;
}

interface MemoTidyModalProps {
  memos: Memo[]; // the selected memos to tidy
  onDone: () => void; // applied → refetch
  onClose: () => void;
}

// The assistant is asked to return STRICT JSON; there's no structured-output plumbing
// (docs/log/21), so we prompt for a fenced array and parse it defensively on the client.
function buildTidyPrompt(memos: Memo[]): string {
  const items = memos.map((m) => ({
    id: m.id,
    repo: m.repo,
    category: m.category,
    // i18n-exempt: memo body fed to an LLM prompt (model behaviour, not display; docs/log/28 §4)
    text: m.kind === "file" ? `対象ファイル ${m.refPath}${m.body ? " — " + m.body : ""}` : m.body,
  }));
  // i18n-exempt-start: LLM prompt (model behaviour, not display; docs/log/28 §4)
  return (
    "あなたはメモ整理アシスタントです。以下は開発者の走り書きメモです。各メモについて、" +
    "(1) 指示として明確な日本語に整形し、(2) サブプロジェクトを表す短いカテゴリ名を提案してください。" +
    "重複や冗長は簡潔にまとめて構いませんが、メモの id は保持し、件数は増やさないでください。\n\n" +
    "出力は次の形の JSON 配列のみ（前後に説明文やコードフェンス以外を付けない）:\n" +
    '[{"id":"<元のid>","cleaned":"<整形後テキスト>","suggestedCategory":"<カテゴリ>"}]\n\n' +
    "メモ:\n" +
    JSON.stringify(items, null, 2)
  );
  // i18n-exempt-end
}

// parseTidyReply pulls the JSON array out of a chat reply that may wrap it in prose or
// a ```json fence.
function parseTidyReply(reply: string): TidyRow[] {
  let s = reply.trim();
  const fence = s.match(/```(?:json)?\s*([\s\S]*?)```/);
  if (fence) s = fence[1].trim();
  const start = s.indexOf("[");
  const end = s.lastIndexOf("]");
  if (start >= 0 && end > start) s = s.slice(start, end + 1);
  const arr = JSON.parse(s);
  if (!Array.isArray(arr)) throw new Error("not an array");
  return arr
    .filter((r) => r && typeof r.id === "string")
    .map((r) => ({
      id: r.id,
      cleaned: typeof r.cleaned === "string" ? r.cleaned : "",
      suggestedCategory: typeof r.suggestedCategory === "string" ? r.suggestedCategory : "",
    }));
}

export function MemoTidyModal({ memos, onDone, onClose }: MemoTidyModalProps) {
  const toast = useToast();
  const tr = useT();
  const [loading, setLoading] = useState(true);
  const [rows, setRows] = useState<TidyRow[]>([]);
  const [pick, setPick] = useState<Record<string, boolean>>({}); // id → apply this row
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const byId = new Map(memos.map((m) => [m.id, m]));

  useEffect(() => {
    let alive = true;
    setLoading(true);
    askAssistant(buildTidyPrompt(memos))
      .then((r) => {
        if (!alive) return;
        if (r.error) {
          setErr(errText(r.error) || tr("sx.tidy_failed"));
          return;
        }
        try {
          const parsed = parseTidyReply(r.reply || "").filter((row) => byId.has(row.id));
          if (parsed.length === 0) {
            setErr(tr("sx.tidy_parse_retry"));
            return;
          }
          setRows(parsed);
          // Default: apply every row whose text or category actually changed.
          const init: Record<string, boolean> = {};
          for (const row of parsed) {
            const m = byId.get(row.id)!;
            init[row.id] = (!!row.cleaned && row.cleaned !== m.body) || row.suggestedCategory !== m.category;
          }
          setPick(init);
        } catch {
          setErr(tr("sx.tidy_parse_nojson"));
        }
      })
      .catch(() => alive && setErr(tr("sx.tidy_failed")))
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const apply = async () => {
    setBusy(true);
    try {
      const chosen = rows.filter((r) => pick[r.id]);
      // apiJSON resolves a server error as {error} rather than throwing, so count the
      // failures or a false "tidied n memos" success toast is shown.
      let failed = 0;
      for (const row of chosen) {
        const m = byId.get(row.id)!;
        const patch: { body?: string; category?: string } = {};
        // For file memos the cleaned text updates the comment (body); text memos update
        // the note itself. Only send fields the user is applying.
        if (row.cleaned && row.cleaned !== m.body) patch.body = row.cleaned;
        if (row.suggestedCategory && row.suggestedCategory !== m.category)
          patch.category = row.suggestedCategory;
        if (Object.keys(patch).length > 0) {
          const res = await memoUpdate(row.id, patch);
          if ((res as { error?: unknown }).error) failed++;
        }
      }
      if (failed > 0) {
        toast(tr("sx.tidy_apply_failed"));
        if (failed < chosen.length) onDone(); // 一部は適用済み — 再取得して反映する
        return;
      }
      toast(tr("sx.tidied", { count: chosen.length }), { kind: "success" });
      onDone();
    } catch {
      toast(tr("sx.tidy_apply_failed"));
    } finally {
      setBusy(false);
    }
  };

  const chosenCount = rows.filter((r) => pick[r.id]).length;

  return (
    <Modal title={tr("sx.tidy_title")} onClose={onClose} className="memo-tidy-modal" lockClose={busy}>
      <div className="ui-modal-body">
        {loading ? (
          <div className="ui-field-hint">{tr("sx.tidy_loading")}</div>
        ) : err ? (
          <div className="ui-field-hint warn">⚠ {err}</div>
        ) : (
          <div className="memo-tidy-list">
            {rows.map((row) => {
              const m = byId.get(row.id)!;
              const before = m.kind === "file" ? `${tr("sx.target_file")} ${m.refPath}${m.body ? " — " + m.body : ""}` : m.body;
              return (
                <label key={row.id} className="memo-tidy-row">
                  <input
                    type="checkbox"
                    checked={!!pick[row.id]}
                    onChange={(e) => setPick((p) => ({ ...p, [row.id]: e.target.checked }))}
                  />
                  <div className="memo-tidy-diff">
                    <div className="memo-tidy-before">{before}</div>
                    <div className="memo-tidy-after">
                      {row.cleaned || <span className="muted">{tr("sx.no_change")}</span>}
                      {row.suggestedCategory && (
                        <span className="memo-cat-tag">
                          {m.category !== row.suggestedCategory ? `${m.category || tr("sx.uncategorized")} → ` : ""}
                          {row.suggestedCategory}
                        </span>
                      )}
                    </div>
                  </div>
                </label>
              );
            })}
          </div>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("sx.cancel")}
        </Button>
        <Button variant="primary" disabled={loading || !!err || busy || chosenCount === 0} onClick={() => void apply()}>
          {chosenCount > 0 ? tr("sx.apply_count", { count: chosenCount }) : tr("sx.apply")}
        </Button>
      </footer>
    </Modal>
  );
}
