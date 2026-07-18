// MemoQueueSection (docs/21, UI刷新) — the left-pane memo queue. Notes accumulate per
// membership and sync across devices (Control-Plane persisted, no server push → refetch
// on mount / store bump + slow poll while mounted). The revamp:
//   - the composer is hidden by default; a header ＋ or leader Ctrl/⌘+K → M reveals it;
//   - a queued memo is editable in place (click to expand full text, click again to edit);
//   - categories are first-class (add empty, rename, delete) and everything reorders by
//     drag — memos within/between categories, and the categories themselves;
//   - "送信…" opens SendMemoModal to edit the concatenated text and pick a destination.
import { useEffect, useMemo, useRef, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t, useT } from "../../lib/i18n/index.ts";
import {
  memoList,
  memoCreate,
  memoUpdate,
  memoDelete,
  memoCategoryList,
  memoCategoryCreate,
  memoCategoryUpdate,
  memoCategoryDelete,
  memoPasteImage,
  memoImageGC,
} from "./api.ts";
import { useMemoStore } from "./store.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useDraft } from "../../lib/draft.ts";
import type { Memo, MemoCategory, MemoAttachment } from "../../types/memo.ts";
import { MemoTidyModal } from "./MemoTidyModal.tsx";
import { SendMemoModal } from "./SendMemoModal.tsx";
import { MemoImageThumb } from "./MemoImageThumb.tsx";
import { consumeShare } from "./share.ts";

const POLL_MS = 10000;
const SECTION_KEY = "af-section-memos"; // shared with the Section component's own key

// A category group as rendered: its memos plus the backing category row id (undefined for
// a legacy category that only exists as a memo.category string, i.e. not reorderable).
interface Group {
  category: string;
  catId?: string;
  memos: Memo[];
}
interface RepoBlock {
  repo: string;
  groups: Group[];
}

const repoLabel = (repo: string) => repo || t("memo.common");
const catLabel = (cat: string) => cat || t("memo.uncategorized");

// Group memos into repo → ordered categories. Category ORDER comes from the categories
// table (by position); the uncategorized bucket leads, then any legacy memo-only category
// is appended. Within a category, memos keep their server order (repo, category, position).
function groupMemos(memos: Memo[], cats: MemoCategory[]): RepoBlock[] {
  const repos: RepoBlock[] = [];
  const repoIx = new Map<string, RepoBlock>();
  const ensureRepo = (repo: string) => {
    let rb = repoIx.get(repo);
    if (!rb) {
      rb = { repo, groups: [] };
      repoIx.set(repo, rb);
      repos.push(rb);
    }
    return rb;
  };
  // Common repo first so the personal/unfiled bucket stays at the top.
  ensureRepo("");

  const byKey = new Map<string, Group>(); // repo\x00category -> group
  const groupFor = (repo: string, category: string, catId?: string) => {
    const key = repo + "\x00" + category;
    let g = byKey.get(key);
    if (!g) {
      g = { category, catId, memos: [] };
      byKey.set(key, g);
      ensureRepo(repo).groups.push(g);
    } else if (catId && !g.catId) {
      g.catId = catId;
    }
    return g;
  };

  // Seed uncategorized buckets and table-defined categories first, in order.
  for (const rb of repos) groupFor(rb.repo, ""); // uncategorized leads (pruned if empty)
  for (const c of cats) groupFor(c.repo, c.name, c.id);

  // Place memos (creating any legacy category groups after the seeded ones).
  for (const m of memos) groupFor(m.repo, m.category).memos.push(m);

  // Drop empty uncategorized buckets, but keep empty NAMED categories (that's the point).
  for (const rb of repos) rb.groups = rb.groups.filter((g) => g.category !== "" || g.memos.length > 0);
  return repos.filter((rb) => rb.groups.length > 0);
}

export function MemoQueueSection() {
  const workspaceRunning = useWorkspaceStore((s) => s.state) === "running";
  const tenant = useTenantStore((s) => s.tenant);
  const memosKey = useMemoStore((s) => s.tick);
  const bumpMemos = useMemoStore((s) => s.bump);
  const composeReq = useMemoStore((s) => s.composeReq);
  const toast = useToast();
  const tr = useT();

  const [memos, setMemos] = useState<Memo[]>([]);
  const [cats, setCats] = useState<MemoCategory[]>([]);
  const [sel, setSel] = useState<Record<string, boolean>>({});
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [editing, setEditing] = useState<string | null>(null);
  const [renameCat, setRenameCat] = useState<string | null>(null); // category id being renamed
  const [composerOpen, setComposerOpen] = useState(false);
  const [newText, setNewText] = useDraft("af.memo-draft");
  const [newCat, setNewCat] = useDraft("af.memo-draft-cat");
  // Images attached to the memo being composed (uploaded to the container immediately;
  // committed into the memo on Add). Not draft-persisted — the bytes live server-side
  // and a page reload would leave them orphaned, so a fresh composer starts empty.
  const [newImages, setNewImages] = useState<MemoAttachment[]>([]);
  const [imgBusy, setImgBusy] = useState(false);
  const imgInputRef = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [tidy, setTidy] = useState<Memo[] | null>(null);
  const [sendOpen, setSendOpen] = useState(false);
  // Section open/closed, owned here (controlled) so the leader command can force it open.
  const [open, setOpen] = useState(() => localStorage.getItem(SECTION_KEY) !== "0");
  const serRef = useRef("");
  const composerTextRef = useRef<HTMLTextAreaElement>(null);

  // Refetch memos + categories on mount / bump / tenant switch, and poll while mounted.
  useEffect(() => {
    let alive = true;
    const load = () =>
      Promise.all([memoList(), memoCategoryList()])
        .then(([mList, cList]) => {
          if (!alive) return;
          const marr = Array.isArray(mList) ? mList : [];
          const carr = Array.isArray(cList) ? cList : [];
          const ser = JSON.stringify([marr, carr]);
          if (ser !== serRef.current) {
            serRef.current = ser;
            setMemos(marr);
            setCats(carr);
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

  // Leader Ctrl/⌘+K → M (or any requestCompose) reveals + focuses the composer.
  useEffect(() => {
    if (composeReq === 0) return;
    setOpen(true);
    setComposerOpen(true);
    requestAnimationFrame(() => composerTextRef.current?.focus());
  }, [composeReq]);

  const setSectionOpen = (o: boolean) => {
    setOpen(o);
    localStorage.setItem(SECTION_KEY, o ? "1" : "0");
  };

  const blocks = useMemo(() => groupMemos(memos, cats), [memos, cats]);
  const unsent = useMemo(() => memos.filter((m) => !m.sentAt), [memos]);
  const catNames = useMemo(() => [...new Set(cats.map((c) => c.name))], [cats]);
  const selectedIds = useMemo(() => memos.filter((m) => sel[m.id]).map((m) => m.id), [memos, sel]);
  const selectedMemos = useMemo(() => memos.filter((m) => sel[m.id]), [memos, sel]);

  const toggle = (id: string) => setSel((s) => ({ ...s, [id]: !s[id] }));

  // ---- composer ----------------------------------------------------------------
  const openComposer = () => {
    setComposerOpen(true);
    requestAnimationFrame(() => composerTextRef.current?.focus());
  };

  // Upload image files (paste / drop / picker) into the container's memo-images dir and
  // append them to the composer's attachments. Non-image files are ignored — memo
  // attachments are images only (a screenshot shared from a phone, a dragged picture).
  const attachImages = async (files: Iterable<File>) => {
    const imgs = [...files].filter((f) => f.type.startsWith("image/"));
    if (!imgs.length) return;
    setImgBusy(true);
    try {
      for (const f of imgs) {
        const res = await memoPasteImage(f);
        if (res.status === 201 && res.path && res.name) {
          setNewImages((cur) => [...cur, { path: res.path!, name: res.name! }]);
        } else {
          toast(res.status === 413 ? t("memo.image_too_large") : t("memo.image_failed"));
        }
      }
    } catch {
      toast(t("memo.image_failed"));
    } finally {
      setImgBusy(false);
    }
  };
  const removeNewImage = (name: string) => setNewImages((cur) => cur.filter((a) => a.name !== name));

  // Best-effort prune of orphaned container images: keep everything referenced by a memo
  // plus the composer's in-flight uploads (not yet committed to a memo). Fire-and-forget.
  const runImageGC = (memoList: Memo[], pending: MemoAttachment[]) => {
    const keep = new Set<string>();
    for (const m of memoList) for (const a of m.attachments || []) keep.add(a.name);
    for (const a of pending) keep.add(a.name);
    void memoImageGC([...keep]).catch(() => {});
  };

  const addMemo = async () => {
    const body = newText.trim();
    if ((!body && newImages.length === 0) || busy) return;
    setBusy(true);
    try {
      const category = newCat.trim();
      const res = await memoCreate({
        kind: "text",
        body,
        category,
        ...(newImages.length ? { attachments: newImages } : {}),
      });
      if ((res as { error?: unknown }).error) {
        toast(t("memo.add_failed"));
        return;
      }
      // A brand-new category becomes first-class so it's reorderable straight away.
      if (category && !catNames.includes(category)) await memoCategoryCreate({ repo: "", name: category }).catch(() => {});
      setNewText("");
      setNewImages([]);
      bumpMemos();
    } catch {
      toast(t("memo.add_failed"));
    } finally {
      setBusy(false);
    }
  };

  // One-shot: a PWA share (Android 共有シート) launches the app at ?share=<id>. Pull the
  // shared text + images out of the SW stash, reveal the composer, and prefill it.
  useEffect(() => {
    let alive = true;
    void consumeShare().then((s) => {
      if (!alive || !s || (!s.text && s.files.length === 0)) return;
      setOpen(true);
      setComposerOpen(true);
      if (s.text) setNewText((prev) => (prev ? prev + "\n" + s.text : s.text));
      if (s.files.length) void attachImages(s.files);
      requestAnimationFrame(() => composerTextRef.current?.focus());
    });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ---- memo edit / delete ------------------------------------------------------
  const saveEdit = async (m: Memo, body: string, category: string) => {
    setEditing(null);
    try {
      await memoUpdate(m.id, { body: body.trim(), category: category.trim() });
      if (category.trim() && !catNames.includes(category.trim()))
        await memoCategoryCreate({ repo: m.repo, name: category.trim() }).catch(() => {});
      bumpMemos();
      toast(t("memo.updated"), { kind: "success" });
    } catch {
      toast(t("common.send_failed"));
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
      // Prune the deleted memo's images (keep every other memo's + composer's in-flight).
      runImageGC(memos.filter((m) => m.id !== id), newImages);
      bumpMemos();
    } catch {
      toast(t("common.delete_failed"));
    }
  };

  // ---- categories --------------------------------------------------------------
  const addCategory = async () => {
    try {
      const res = await memoCategoryCreate({ repo: "", name: t("memo.new_category") });
      bumpMemos();
      if (res && res.id) setRenameCat(res.id);
    } catch {
      toast(t("memo.add_failed"));
    }
  };
  const commitRename = async (c: MemoCategory, name: string) => {
    setRenameCat(null);
    const next = name.trim();
    if (!next || next === c.name) return;
    try {
      await memoCategoryUpdate(c.id, { name: next });
      bumpMemos();
    } catch {
      toast(t("common.send_failed"));
    }
  };
  const deleteCategory = async (c: MemoCategory) => {
    if (!confirm(t("memo.cat_delete_confirm", { name: c.name }))) return;
    try {
      await memoCategoryDelete(c.id);
      bumpMemos();
      toast(t("memo.cat_deleted"));
    } catch {
      toast(t("common.delete_failed"));
    }
  };

  // ---- drag & drop -------------------------------------------------------------
  // Transient drag subjects (a memo id, or a category id). Native HTML5 DnD.
  const dragMemo = useRef<string | null>(null);
  const dragCat = useRef<string | null>(null);
  const [dropCat, setDropCat] = useState<string | null>(null); // repo\x00category highlighted

  // Persist the position (and repo/category) of every memo in the given groups, matching
  // the current local order — a few small PATCHes, then a bump to reconcile with the server.
  const persistGroups = async (list: Memo[], groups: { repo: string; category: string }[]) => {
    const patches: Promise<unknown>[] = [];
    for (const { repo, category } of groups) {
      const grp = list.filter((m) => m.repo === repo && m.category === category);
      grp.forEach((m, i) => patches.push(memoUpdate(m.id, { position: i, category, repo })));
    }
    try {
      await Promise.all(patches);
    } finally {
      bumpMemos();
    }
  };

  // Reorder a memo before another memo (adopting the target's repo/category if different).
  const dropMemoOnMemo = (dragId: string, targetId: string) => {
    if (dragId === targetId) return;
    const arr = memos.slice();
    const di = arr.findIndex((m) => m.id === dragId);
    const ti = arr.findIndex((m) => m.id === targetId);
    if (di < 0 || ti < 0) return;
    const [moved] = arr.splice(di, 1);
    const target = arr.find((m) => m.id === targetId)!;
    const changedGroups = [{ repo: moved.repo, category: moved.category }];
    moved.repo = target.repo;
    moved.category = target.category;
    const insertAt = arr.findIndex((m) => m.id === targetId);
    arr.splice(insertAt, 0, moved);
    setMemos(arr);
    changedGroups.push({ repo: target.repo, category: target.category });
    void persistGroups(arr, dedupeGroups(changedGroups));
  };

  // Move a memo to the end of a (repo, category) group.
  const dropMemoOnGroup = (dragId: string, repo: string, category: string) => {
    const arr = memos.slice();
    const di = arr.findIndex((m) => m.id === dragId);
    if (di < 0) return;
    const [moved] = arr.splice(di, 1);
    const from = { repo: moved.repo, category: moved.category };
    if (moved.repo === repo && moved.category === category) {
      arr.splice(di, 0, moved); // no-op drop onto own group
      return;
    }
    moved.repo = repo;
    moved.category = category;
    arr.push(moved);
    setMemos(arr);
    if (category) toast(t("memo.moved", { cat: category }));
    void persistGroups(arr, dedupeGroups([from, { repo, category }]));
  };

  // Reorder categories within a repo by rewriting positions.
  const dropCatOnCat = (dragId: string, targetId: string) => {
    if (dragId === targetId) return;
    const drag = cats.find((c) => c.id === dragId);
    const target = cats.find((c) => c.id === targetId);
    if (!drag || !target || drag.repo !== target.repo) return;
    const repoCats = cats.filter((c) => c.repo === drag.repo).slice();
    const di = repoCats.findIndex((c) => c.id === dragId);
    repoCats.splice(di, 1);
    const ti = repoCats.findIndex((c) => c.id === targetId);
    repoCats.splice(ti, 0, drag);
    setCats((prev) => prev.map((c) => c.repo === drag.repo ? repoCats.find((x) => x.id === c.id) || c : c));
    Promise.all(repoCats.map((c, i) => (c.position === i ? null : memoCategoryUpdate(c.id, { position: i }))))
      .catch(() => {})
      .finally(bumpMemos);
  };

  const selectGroup = (ids: string[]) => setSel((s) => {
    const n = { ...s };
    for (const id of ids) n[id] = true;
    return n;
  });
  const sendGroup = (ids: string[]) => {
    if (ids.length === 0) return;
    selectGroup(ids);
    setSendOpen(true);
  };

  const actions = (
    <>
      <Button
        small
        variant="ghost"
        icon="add"
        title={tr("memo.add_title")}
        aria-label={tr("memo.add_title")}
        onClick={openComposer}
      />
      <Button
        small
        variant="ghost"
        icon="sparkle"
        title={workspaceRunning ? tr("memo.organize_title") : tr("memo.organize_needs_ws")}
        aria-label={tr("memo.organize_title")}
        disabled={!workspaceRunning || selectedIds.length === 0}
        onClick={() => {
          if (selectedMemos.length === 0) {
            toast(t("memo.select_to_organize"), { kind: "warn" });
            return;
          }
          setTidy(selectedMemos);
        }}
      />
    </>
  );

  return (
    <>
      <Section
        id="memos"
        title={tr("memo.title")}
        icon="checklist"
        count={unsent.length}
        actions={actions}
        open={open}
        onToggle={() => setSectionOpen(!open)}
      >
        {composerOpen && (
          <div
            className="memo-add"
            onDragOver={(e) => {
              if (e.dataTransfer.types.includes("Files")) e.preventDefault();
            }}
            onDrop={(e) => {
              if (e.dataTransfer.files.length) {
                e.preventDefault();
                void attachImages(e.dataTransfer.files);
              }
            }}
          >
            <textarea
              ref={composerTextRef}
              className="memo-add-text"
              value={newText}
              rows={2}
              placeholder={tr("memo.add_ph")}
              onChange={(e) => setNewText(e.target.value)}
              onPaste={(e) => {
                const imgs = [...e.clipboardData.files].filter((f) => f.type.startsWith("image/"));
                if (imgs.length) {
                  e.preventDefault();
                  void attachImages(imgs);
                }
              }}
              onKeyDown={(e) => {
                if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
                  e.preventDefault();
                  void addMemo();
                } else if (e.key === "Escape") {
                  setComposerOpen(false);
                }
              }}
            />
            {(newImages.length > 0 || imgBusy) && (
              <div className="memo-add-thumbs">
                {newImages.map((a) => (
                  <MemoImageThumb key={a.name} name={a.name} onRemove={() => removeNewImage(a.name)} />
                ))}
                {imgBusy && (
                  <span className="memo-thumb loading">
                    <Icon name="loading" spin />
                  </span>
                )}
              </div>
            )}
            <div className="memo-add-row">
              <input
                className="memo-add-cat"
                list="memo-cat-suggest"
                value={newCat}
                placeholder={tr("memo.category_ph")}
                onChange={(e) => setNewCat(e.target.value)}
                onKeyDown={(e) => {
                  if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
                    e.preventDefault();
                    void addMemo();
                  } else if (e.key === "Escape") setComposerOpen(false);
                }}
              />
              <input
                ref={imgInputRef}
                type="file"
                accept="image/*"
                multiple
                hidden
                onChange={(e) => {
                  if (e.target.files?.length) void attachImages(e.target.files);
                  e.target.value = "";
                }}
              />
              <Button
                small
                variant="ghost"
                title={tr("memo.attach_image")}
                aria-label={tr("memo.attach_image")}
                disabled={imgBusy}
                onClick={() => imgInputRef.current?.click()}
              >
                <Icon name="device-camera" />
              </Button>
              <Button
                small
                variant="primary"
                disabled={(!newText.trim() && newImages.length === 0) || busy}
                onClick={() => void addMemo()}
              >
                {tr("memo.add")}
              </Button>
            </div>
            <div className="memo-add-hint">{tr("memo.composer_hint")}</div>
            <datalist id="memo-cat-suggest">
              {catNames.map((c) => (
                <option key={c} value={c} />
              ))}
            </datalist>
          </div>
        )}

        {memos.length === 0 && cats.length === 0 ? (
          <div className="pane-empty">{tr("memo.empty")}</div>
        ) : (
          <>
            {blocks.map((rb) => (
              <div key={rb.repo} className="memo-repo">
                {rb.repo && (
                  <div className="memo-repo-head">
                    <Icon name="repo" className="memo-repo-ic" />
                    {repoLabel(rb.repo)}
                  </div>
                )}
                {rb.groups.map((g) => {
                  const ids = g.memos.map((m) => m.id);
                  const allSel = ids.length > 0 && ids.every((id) => sel[id]);
                  const cat = g.catId ? cats.find((c) => c.id === g.catId) : undefined;
                  const dropKey = rb.repo + "\x00" + g.category;
                  return (
                    <div
                      key={g.category || "\x00unfiled"}
                      className={"memo-cat" + (dropCat === dropKey ? " drop" : "")}
                      onDragOver={(e) => {
                        if (dragMemo.current || dragCat.current) {
                          e.preventDefault();
                          setDropCat(dropKey);
                        }
                      }}
                      onDragLeave={(e) => {
                        if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) setDropCat(null);
                      }}
                      onDrop={(e) => {
                        e.preventDefault();
                        setDropCat(null);
                        if (dragMemo.current) dropMemoOnGroup(dragMemo.current, rb.repo, g.category);
                        else if (dragCat.current && cat) dropCatOnCat(dragCat.current, cat.id);
                      }}
                    >
                      <div className="memo-cat-head">
                        {cat && (
                          <span
                            className="memo-cat-grip"
                            draggable
                            title={tr("memo.reorder_category")}
                            onDragStart={() => (dragCat.current = cat.id)}
                            onDragEnd={() => (dragCat.current = null)}
                          >
                            <Icon name="gripper" />
                          </span>
                        )}
                        <input
                          type="checkbox"
                          className="memo-cat-check"
                          checked={allSel}
                          disabled={ids.length === 0}
                          onChange={() =>
                            setSel((s) => {
                              const n = { ...s };
                              for (const id of ids) n[id] = !allSel;
                              return n;
                            })
                          }
                        />
                        {cat && renameCat === cat.id ? (
                          <input
                            className="memo-cat-rename"
                            defaultValue={cat.name}
                            autoFocus
                            onBlur={(e) => void commitRename(cat, e.target.value)}
                            onKeyDown={(e) => {
                              if (e.key === "Enter") (e.target as HTMLInputElement).blur();
                              else if (e.key === "Escape") setRenameCat(null);
                            }}
                          />
                        ) : (
                          <span
                            className="memo-cat-name"
                            title={cat ? tr("memo.rename_category") : undefined}
                            onClick={() => cat && setRenameCat(cat.id)}
                          >
                            {catLabel(g.category)}
                          </span>
                        )}
                        <span className="memo-cat-n">{g.memos.length}</span>
                        {ids.length > 0 && (
                          <button type="button" className="memo-cat-send linkish sm" onClick={() => sendGroup(ids)}>
                            {tr("common.send")}
                          </button>
                        )}
                        {cat && (
                          <button
                            type="button"
                            className="memo-cat-del"
                            title={tr("memo.delete_category")}
                            aria-label={tr("memo.delete_category")}
                            onClick={() => void deleteCategory(cat)}
                          >
                            <Icon name="trash" />
                          </button>
                        )}
                      </div>

                      {g.memos.map((m) => (
                        <MemoRow
                          key={m.id}
                          memo={m}
                          selected={!!sel[m.id]}
                          expanded={!!expanded[m.id]}
                          editing={editing === m.id}
                          currentCat={g.category}
                          onToggle={() => toggle(m.id)}
                          onExpand={() => setExpanded((s) => ({ ...s, [m.id]: true }))}
                          onEdit={() => setEditing(m.id)}
                          onCancelEdit={() => setEditing(null)}
                          onSave={(body, category) => void saveEdit(m, body, category)}
                          onDelete={() => void remove(m.id)}
                          onDragStart={() => (dragMemo.current = m.id)}
                          onDragEnd={() => {
                            dragMemo.current = null;
                            setDropCat(null);
                          }}
                          onDropBefore={() => {
                            if (dragMemo.current) dropMemoOnMemo(dragMemo.current, m.id);
                          }}
                        />
                      ))}
                    </div>
                  );
                })}
              </div>
            ))}

            <button type="button" className="memo-addcat" onClick={() => void addCategory()}>
              <Icon name="add" /> {tr("memo.add_category")}
            </button>
          </>
        )}

        {selectedIds.length > 0 && (
          <div className="memo-selbar">
            <span className="memo-selbar-n">{tr("memo.selected_n", { count: selectedIds.length })}</span>
            <Button small variant="ghost" className="memo-selbar-clear" onClick={() => setSel({})}>
              {tr("memo.clear_selection")}
            </Button>
            <Button small variant="primary" onClick={() => setSendOpen(true)}>
              {tr("memo.open_send")}
            </Button>
          </div>
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
      {sendOpen && selectedMemos.length > 0 && (
        <SendMemoModal
          memos={selectedMemos}
          onClose={() => setSendOpen(false)}
          onSent={() => {
            setSel({});
            setSendOpen(false);
            bumpMemos();
          }}
        />
      )}
    </>
  );
}

function dedupeGroups(groups: { repo: string; category: string }[]): { repo: string; category: string }[] {
  const seen = new Set<string>();
  return groups.filter((g) => {
    const k = g.repo + "\x00" + g.category;
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
}

// One memo row: collapsed to 2 lines, click to expand full text, click again to edit.
interface MemoRowProps {
  memo: Memo;
  selected: boolean;
  expanded: boolean;
  editing: boolean;
  currentCat: string;
  onToggle: () => void;
  onExpand: () => void;
  onEdit: () => void;
  onCancelEdit: () => void;
  onSave: (body: string, category: string) => void;
  onDelete: () => void;
  onDragStart: () => void;
  onDragEnd: () => void;
  onDropBefore: () => void;
}

function MemoRow(props: MemoRowProps) {
  const { memo: m, selected, expanded, editing, currentCat } = props;
  const tr = useT();
  const [dragOver, setDragOver] = useState(false);
  const [body, setBody] = useState(m.body);
  const [cat, setCat] = useState(currentCat);
  const taRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    if (editing) {
      setBody(m.body);
      setCat(currentCat);
      requestAnimationFrame(() => {
        const ta = taRef.current;
        if (ta) {
          ta.focus();
          ta.setSelectionRange(ta.value.length, ta.value.length);
        }
      });
    }
  }, [editing, m.body, currentCat]);

  if (editing) {
    return (
      <div className="memo-row editing">
        <div className="memo-edit">
          <textarea
            ref={taRef}
            className="memo-edit-text"
            value={body}
            rows={3}
            onChange={(e) => setBody(e.target.value)}
            onKeyDown={(e) => {
              if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
                e.preventDefault();
                props.onSave(body, cat);
              } else if (e.key === "Escape") props.onCancelEdit();
            }}
          />
          <div className="memo-edit-row">
            <input
              className="memo-edit-cat"
              list="memo-cat-suggest"
              value={cat}
              placeholder={tr("memo.category_ph")}
              onChange={(e) => setCat(e.target.value)}
            />
            <Button small variant="primary" onClick={() => props.onSave(body, cat)}>
              {tr("memo.save")}
            </Button>
            <Button small variant="ghost" onClick={props.onCancelEdit}>
              {tr("common.cancel")}
            </Button>
          </div>
          <div className="memo-edit-hint">{m.kind === "file" ? tr("memo.edit_hint_file") : tr("memo.edit_hint")}</div>
        </div>
      </div>
    );
  }

  const isLong = (m.body || "").length > 60 || (m.body || "").includes("\n");
  return (
    <div
      className={
        "memo-row" +
        (selected ? " sel" : "") +
        (m.sentAt ? " sent" : "") +
        (expanded ? " exp" : "") +
        (dragOver ? " dragover" : "")
      }
      onDragOver={(e) => {
        e.preventDefault();
        setDragOver(true);
      }}
      onDragLeave={() => setDragOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        e.stopPropagation();
        setDragOver(false);
        props.onDropBefore();
      }}
    >
      <span
        className="memo-grip"
        draggable
        title={tr("memo.reorder_memo")}
        onDragStart={props.onDragStart}
        onDragEnd={props.onDragEnd}
      >
        <Icon name="gripper" />
      </span>
      <input type="checkbox" checked={selected} onChange={props.onToggle} />
      <div
        className="memo-body"
        onClick={(e) => {
          if ((e.target as HTMLElement).closest("code")) return;
          if (!expanded) props.onExpand();
          else props.onEdit();
        }}
      >
        {m.kind === "file" ? (
          <>
            <code className="memo-ref">{m.refPath}</code>
            {m.body && <div className="memo-comment memo-text">{m.body}</div>}
          </>
        ) : (
          m.body && <div className="memo-text">{m.body}</div>
        )}
        {m.attachments && m.attachments.length > 0 && (
          <div className="memo-row-thumbs" onClick={(e) => e.stopPropagation()}>
            {m.attachments.map((a) => (
              <MemoImageThumb key={a.name} name={a.name} />
            ))}
          </div>
        )}
        {(m.sentAt || (!expanded && isLong)) && (
          <div className="memo-meta">
            {m.sentAt && <span className="memo-sent-tag">{tr("memo.sent_tag")}</span>}
            {!expanded && isLong && <span className="memo-more">{tr("memo.expand_hint")}</span>}
          </div>
        )}
      </div>
      <button type="button" className="memo-del" title={tr("common.delete")} aria-label={tr("common.delete")} onClick={props.onDelete}>
        <Icon name="trash" />
      </button>
    </div>
  );
}
