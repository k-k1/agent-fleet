// MemoQueueSection (docs/21) — ported from the old sections/MemoQueueSection.tsx
// (docs/22 P6c). A left-pane panel of notes accumulated per membership and synced
// across devices, grouped by repo → category, then flushed to a running session as
// one concatenated message. Persisted in the Control Plane, so there's no server
// push — we refetch on mount / store bump and poll on a slow interval while mounted.
import { useEffect, useMemo, useRef, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { errText } from "../../core/api/client.ts";
import { memoList, memoCreate, memoDelete, memoFlush } from "./api.ts";
import { useMemoStore } from "./store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { agentOf } from "../../agents/registry.ts";
import { useDraft } from "../../lib/draft.ts";
import { displayName, stateInfo } from "../../lib/sessionview.ts";
import type { Memo } from "../../types/memo.ts";
import type { Session } from "../../types/session.ts";
import { MemoTidyModal } from "./MemoTidyModal.tsx";

const POLL_MS = 10000;

// A repo bucket holds its categories in insertion order (the server returns memos
// sorted by repo, category, position, so iterating preserves grouping).
interface CatGroup {
  category: string;
  memos: Memo[];
}
interface RepoGroup {
  repo: string;
  cats: CatGroup[];
}

function groupMemos(memos: Memo[]): RepoGroup[] {
  const repos: RepoGroup[] = [];
  const repoIx = new Map<string, RepoGroup>();
  const catIx = new Map<string, CatGroup>();
  for (const m of memos) {
    let rg = repoIx.get(m.repo);
    if (!rg) {
      rg = { repo: m.repo, cats: [] };
      repoIx.set(m.repo, rg);
      repos.push(rg);
    }
    const ck = m.repo + "\x00" + m.category;
    let cg = catIx.get(ck);
    if (!cg) {
      cg = { category: m.category, memos: [] };
      catIx.set(ck, cg);
      rg.cats.push(cg);
    }
    cg.memos.push(m);
  }
  return repos;
}

const repoLabel = (repo: string) => repo || "共通";
const catLabel = (cat: string) => cat || "未分類";

export function MemoQueueSection() {
  const workspaceRunning = useWorkspaceStore((s) => s.state) === "running";
  const sessions = useSessionsStore((s) => s.sessions);
  const tenant = useTenantStore((s) => s.tenant);
  const memosKey = useMemoStore((s) => s.tick);
  const bumpMemos = useMemoStore((s) => s.bump);
  const toast = useToast();
  const [memos, setMemos] = useState<Memo[]>([]);
  const [sel, setSel] = useState<Record<string, boolean>>({});
  const [target, setTarget] = useState(""); // session name to flush to
  // The in-progress memo (text + category) persists in localStorage, so a reload — or
  // the browser dying — doesn't lose a note that was being typed.
  const [newText, setNewText] = useDraft("af.memo-draft");
  const [newCat, setNewCat] = useDraft("af.memo-draft-cat");
  const [busy, setBusy] = useState(false);
  const [tidy, setTidy] = useState<Memo[] | null>(null); // memos handed to the tidy modal
  const serRef = useRef("");

  // Refetch on mount + bump + tenant switch, and poll while mounted (cross-device
  // sync; a background GET doesn't warm the container, so this can't keep a workspace up).
  useEffect(() => {
    let alive = true;
    const load = () =>
      memoList()
        .then((list) => {
          if (!alive) return;
          const arr = Array.isArray(list) ? list : [];
          const ser = JSON.stringify(arr);
          if (ser !== serRef.current) {
            serRef.current = ser;
            setMemos(arr);
          }
        })
        .catch(() => {});
    load();
    const id = setInterval(load, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [memosKey, tenant]);

  const groups = useMemo(() => groupMemos(memos), [memos]);
  const unsent = useMemo(() => memos.filter((m) => !m.sentAt), [memos]);

  // Flush target ranking, mirroring SendSelectionModal: a session is 入力待ち when it's
  // an alive chat-capable agent sitting idle. Default to the highest-ranked alive one.
  const isWaiting = (s: Session) => !!s.alive && agentOf(s.kind).caps.chat && (!s.state || s.state === "idle");
  const aliveSessions = useMemo(
    () => sessions.filter((s) => s.alive).slice().sort((a, b) => (isWaiting(b) ? 1 : 0) - (isWaiting(a) ? 1 : 0)),
    [sessions],
  );
  useEffect(() => {
    if (target && aliveSessions.some((s) => s.name === target)) return;
    setTarget(aliveSessions[0]?.name || "");
  }, [aliveSessions, target]);

  const toggle = (id: string) => setSel((s) => ({ ...s, [id]: !s[id] }));
  const selectedIds = useMemo(() => memos.filter((m) => sel[m.id]).map((m) => m.id), [memos, sel]);

  const flush = async (ids: string[]) => {
    if (ids.length === 0) return;
    if (!target) {
      toast("送信先の稼働セッションがありません。セッションを起動してください。", { kind: "warn" });
      return;
    }
    setBusy(true);
    try {
      const res = await memoFlush(target, ids);
      if (res.error) {
        toast(errText(res.error) || "送信に失敗しました");
        return;
      }
      const s = sessions.find((x) => x.name === target);
      toast(`${res.sent ?? ids.length} 件を ${s ? displayName(s) : target} に送信しました`, { kind: "success" });
      setSel({});
      bumpMemos();
    } catch {
      toast("送信に失敗しました");
    } finally {
      setBusy(false);
    }
  };

  const addMemo = async () => {
    const body = newText.trim();
    if (!body || busy) return;
    setBusy(true);
    try {
      const res = await memoCreate({ kind: "text", body, category: newCat.trim() });
      if ((res as { error?: unknown }).error) {
        toast("メモの追加に失敗しました");
        return;
      }
      setNewText("");
      bumpMemos();
    } catch {
      toast("メモの追加に失敗しました");
    } finally {
      setBusy(false);
    }
  };

  const remove = async (id: string) => {
    try {
      await memoDelete(id);
      setSel((s) => {
        const n = { ...s };
        delete n[id];
        return n;
      });
      bumpMemos();
    } catch {
      toast("削除に失敗しました");
    }
  };

  const openTidy = (ids: string[]) => {
    const picked = memos.filter((m) => ids.includes(m.id));
    if (picked.length === 0) {
      toast("整理するメモを選択してください", { kind: "warn" });
      return;
    }
    setTidy(picked);
  };

  const actions = (
    <Button
      small
      variant="ghost"
      icon="sparkle"
      title={workspaceRunning ? "選択したメモをアシスタントで整理" : "整理にはワークスペースの起動が必要です"}
      disabled={!workspaceRunning || selectedIds.length === 0}
      onClick={() => openTidy(selectedIds)}
    />
  );

  return (
    <>
      <Section id="memos" title="メモキュー" icon="checklist" count={unsent.length} actions={actions}>
        <div className="memo-add">
          <textarea
            className="memo-add-text"
            value={newText}
            rows={2}
            placeholder="走り書きメモを追加…（後でまとめて送信）"
            onChange={(e) => setNewText(e.target.value)}
            onKeyDown={(e) => {
              if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
                e.preventDefault();
                void addMemo();
              }
            }}
          />
          <div className="memo-add-row">
            <input
              className="memo-add-cat"
              list="memo-cat-suggest"
              value={newCat}
              placeholder="カテゴリ（任意）"
              onChange={(e) => setNewCat(e.target.value)}
            />
            <Button small variant="ghost" disabled={!newText.trim() || busy} onClick={() => void addMemo()}>
              追加
            </Button>
          </div>
          <datalist id="memo-cat-suggest">
            {[...new Set(memos.map((m) => m.category).filter(Boolean))].map((c) => (
              <option key={c} value={c} />
            ))}
          </datalist>
        </div>

        {memos.length === 0 ? (
          <div className="pane-empty">メモはまだありません。ファイルの右クリックや上の入力から溜められます。</div>
        ) : (
          <>
            <div className="memo-flush-bar">
              <select value={target} onChange={(e) => setTarget(e.target.value)} disabled={aliveSessions.length === 0}>
                {aliveSessions.length === 0 ? (
                  <option value="">稼働セッションなし</option>
                ) : (
                  aliveSessions.map((s) => (
                    <option key={s.name} value={s.name}>
                      {displayName(s)}（{stateInfo(s).text}）
                    </option>
                  ))
                )}
              </select>
              <Button
                small
                variant="primary"
                disabled={selectedIds.length === 0 || busy || !target}
                onClick={() => void flush(selectedIds)}
              >
                選択を送信{selectedIds.length > 0 ? `（${selectedIds.length}）` : ""}
              </Button>
            </div>

            {groups.map((rg) => (
              <div key={rg.repo} className="memo-repo">
                <div className="memo-repo-head">
                  <Icon name="repo" className="memo-repo-ic" />
                  {repoLabel(rg.repo)}
                </div>
                {rg.cats.map((cg) => {
                  const ids = cg.memos.map((m) => m.id);
                  const allSel = ids.every((id) => sel[id]);
                  return (
                    <div key={cg.category} className="memo-cat">
                      <div className="memo-cat-head">
                        <label className="memo-cat-check">
                          <input
                            type="checkbox"
                            checked={allSel}
                            onChange={() =>
                              setSel((s) => {
                                const n = { ...s };
                                for (const id of ids) n[id] = !allSel;
                                return n;
                              })
                            }
                          />
                          {catLabel(cg.category)}
                          <span className="memo-cat-n">{cg.memos.length}</span>
                        </label>
                        <button
                          type="button"
                          className="linkish sm"
                          title="このカテゴリをまとめて送信"
                          disabled={busy || !target}
                          onClick={() => void flush(ids)}
                        >
                          送信
                        </button>
                      </div>
                      {cg.memos.map((m) => (
                        <div key={m.id} className={"memo-row" + (m.sentAt ? " sent" : "")}>
                          <input type="checkbox" checked={!!sel[m.id]} onChange={() => toggle(m.id)} />
                          <div className="memo-body">
                            {m.kind === "file" ? (
                              <>
                                <code className="memo-ref">{m.refPath}</code>
                                {m.body && <div className="memo-comment">{m.body}</div>}
                              </>
                            ) : (
                              <div className="memo-text">{m.body}</div>
                            )}
                            {m.sentAt && <span className="memo-sent-tag">送信済み</span>}
                          </div>
                          <button type="button" className="memo-del" title="削除" onClick={() => void remove(m.id)}>
                            <Icon name="trash" />
                          </button>
                        </div>
                      ))}
                    </div>
                  );
                })}
              </div>
            ))}
          </>
        )}
      </Section>

      {tidy && (
        <MemoTidyModal
          memos={tidy}
          onClose={() => setTidy(null)}
          onDone={() => {
            setTidy(null);
            bumpMemos();
          }}
        />
      )}
    </>
  );
}
