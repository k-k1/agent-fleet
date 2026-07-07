import { useEffect, useState } from "react";
import Modal from "../Modal.jsx";
import { useToast } from "../ToastProvider.jsx";
import { askAssistant, memoUpdate, errText } from "../../api.js";
import type { Memo } from "../../types/memo.ts";

// MemoTidyModal (docs/21 整理): hands the selected memos to a stateless assistant turn
// that returns cleaned text + a suggested category per memo, previews old→new, and on
// approval PATCHes the changes. We never auto-apply — a bad tidy shouldn't need undo.

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
// (docs/21), so we prompt for a fenced array and parse it defensively on the client.
function buildTidyPrompt(memos: Memo[]): string {
  const items = memos.map((m) => ({
    id: m.id,
    repo: m.repo,
    category: m.category,
    text: m.kind === "file" ? `対象ファイル ${m.refPath}${m.body ? " — " + m.body : ""}` : m.body,
  }));
  return (
    "あなたはメモ整理アシスタントです。以下は開発者の走り書きメモです。各メモについて、" +
    "(1) 指示として明確な日本語に整形し、(2) サブプロジェクトを表す短いカテゴリ名を提案してください。" +
    "重複や冗長は簡潔にまとめて構いませんが、メモの id は保持し、件数は増やさないでください。\n\n" +
    "出力は次の形の JSON 配列のみ（前後に説明文やコードフェンス以外を付けない）:\n" +
    '[{"id":"<元のid>","cleaned":"<整形後テキスト>","suggestedCategory":"<カテゴリ>"}]\n\n' +
    "メモ:\n" +
    JSON.stringify(items, null, 2)
  );
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

export default function MemoTidyModal({ memos, onDone, onClose }: MemoTidyModalProps) {
  const toast = useToast();
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
          setErr(errText(r.error) || "整理に失敗しました");
          return;
        }
        try {
          const parsed = parseTidyReply(r.reply || "").filter((row) => byId.has(row.id));
          if (parsed.length === 0) {
            setErr("整理結果を解釈できませんでした（もう一度お試しください）。");
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
          setErr("整理結果を解釈できませんでした（JSON を返しませんでした）。");
        }
      })
      .catch(() => alive && setErr("整理に失敗しました"))
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
      for (const row of chosen) {
        const m = byId.get(row.id)!;
        const patch: { body?: string; category?: string } = {};
        // For file memos the cleaned text updates the comment (body); text memos update
        // the note itself. Only send fields the user is applying.
        if (row.cleaned && row.cleaned !== m.body) patch.body = row.cleaned;
        if (row.suggestedCategory && row.suggestedCategory !== m.category)
          patch.category = row.suggestedCategory;
        if (Object.keys(patch).length > 0) await memoUpdate(row.id, patch);
      }
      toast(`${chosen.length} 件を整理しました`, { kind: "success" });
      onDone();
    } catch {
      toast("整理の反映に失敗しました");
    } finally {
      setBusy(false);
    }
  };

  const chosenCount = rows.filter((r) => pick[r.id]).length;

  return (
    <Modal title="アシスタントで整理" onClose={onClose} className="memo-tidy-modal" lockClose={busy}>
      <div className="modal-body">
        {loading ? (
          <div className="field-help">整理中…（アシスタントに問い合わせています）</div>
        ) : err ? (
          <div className="field-help field-warn">⚠ {err}</div>
        ) : (
          <div className="memo-tidy-list">
            {rows.map((row) => {
              const m = byId.get(row.id)!;
              const before = m.kind === "file" ? `対象ファイル ${m.refPath}${m.body ? " — " + m.body : ""}` : m.body;
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
                      {row.cleaned || <span className="muted">（変更なし）</span>}
                      {row.suggestedCategory && (
                        <span className="memo-cat-tag">
                          {m.category !== row.suggestedCategory ? `${m.category || "未分類"} → ` : ""}
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
      <footer className="modal-foot">
        <button type="button" className="ghost" onClick={onClose}>
          キャンセル
        </button>
        <button
          type="button"
          className="primary"
          disabled={loading || !!err || busy || chosenCount === 0}
          onClick={apply}
        >
          {chosenCount > 0 ? `${chosenCount} 件を反映` : "反映"}
        </button>
      </footer>
    </Modal>
  );
}
