// KnowledgeMemo — the workspace's notes on a family and on a model (ADR 0100 decision 12),
// under the family card. Four sections. Where the browse root is home the Files pane reaches the
// file, and this memo reads it and appends to "records" (the same route add_image_knowledge
// uses). Where it is not (the Agent sends no `files_path`), nothing else in the Console can reach
// the file, so the memo becomes the editor of all four sections.
//
// Read on demand (the memo is folded by default), never on the studio's 2 s poll: the summary
// already rides every get_image_studio, and the pane has no reason to pay for it per tick.
import { useCallback, useEffect, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { openFileMode } from "../../viewer/openFile.ts";
import { addImagegenKnowledge, editImagegenKnowledge, imagegenKnowledge, type Knowledge, type KnowledgeScope } from "../api.ts";

const SECTIONS = ["summary", "settings", "prompts", "records"] as const;
type Section = (typeof SECTIONS)[number];

/** A path the Files pane can open: relative to the browse root. An absolute one is outside it. */
export const openableKnowledgePath = (p: string | undefined): string | null =>
  p && !p.startsWith("/") && !p.startsWith("~") && !p.split("/").includes("..") ? p : null;

export function KnowledgeMemo({ family, model }: { family?: string; model?: string }) {
  const tr = useT();
  const targets: { scope: KnowledgeScope; key: string }[] = [
    ...(family ? [{ scope: "family" as const, key: family }] : []),
    ...(model ? [{ scope: "model" as const, key: model }] : []),
  ];
  const [open, setOpen] = useState(false);
  if (!targets.length) return null;
  return (
    <details className="igen-memo" onToggle={(e) => setOpen((e.currentTarget as HTMLDetailsElement).open)}>
      <summary>{tr("imggen.memo")}</summary>
      {/* Mounted only while open: a closed <details> still renders its children, and each doc
          reads its file on mount. */}
      {open && targets.map((t) => <KnowledgeDoc key={`${t.scope}:${t.key}`} scope={t.scope} docKey={t.key} />)}
    </details>
  );
}

function KnowledgeDoc({ scope, docKey }: { scope: KnowledgeScope; docKey: string }) {
  const tr = useT();
  const toast = useToast();
  const [doc, setDoc] = useState<Knowledge | null>(null);
  const [err, setErr] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<Record<Section, string> | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await imagegenKnowledge(scope, docKey);
      if (!r || r.error) {
        setErr((r?.error && errText(r.error)) || tr("imggen.memo_failed"));
        return;
      }
      setErr("");
      setDoc(r);
    } catch {
      setErr(tr("imggen.memo_failed"));
    }
  }, [scope, docKey, tr]);

  useEffect(() => {
    void load();
  }, [load]);

  const add = async () => {
    const text = note.trim();
    if (!text) return;
    setBusy(true);
    try {
      const r = await addImagegenKnowledge({ scope, key: docKey, note: text });
      if (r?.error) toast(errText(r.error) || tr("imggen.memo_add_failed"), { kind: "error" });
      else {
        setNote("");
        await load();
      }
    } catch {
      toast(tr("imggen.memo_add_failed"), { kind: "error" });
    } finally {
      setBusy(false);
    }
  };

  const save = async () => {
    if (!editing || !doc) return;
    setBusy(true);
    try {
      const r = await editImagegenKnowledge({ scope, key: docKey, version: doc.version || "", ...editing });
      if (r.status === 412) {
        // The agent's Edit or add_image_knowledge got there first: keep what was typed, show
        // the file as it is now, and let the member merge by hand.
        toast(tr("imggen.memo_changed"), { kind: "error" });
        await load();
      } else if (r.error || r.status !== 200) {
        toast((r.error && errText(r.error)) || tr("imggen.memo_save_failed"), { kind: "error" });
      } else {
        setDoc(r);
        setEditing(null);
        toast(tr("imggen.memo_saved"), { kind: "info" });
      }
    } finally {
      setBusy(false);
    }
  };

  // Editing starts from the whole file: the read the memo shows has its summary cut.
  const startEdit = async () => {
    setBusy(true);
    try {
      const r = await imagegenKnowledge(scope, docKey, true);
      if (!r || r.error) {
        toast((r?.error && errText(r.error)) || tr("imggen.memo_failed"), { kind: "error" });
        return;
      }
      setDoc(r);
      setEditing({ summary: r.summary || "", settings: r.settings || "", prompts: r.prompts || "", records: r.records || "" });
    } catch {
      toast(tr("imggen.memo_failed"), { kind: "error" });
    } finally {
      setBusy(false);
    }
  };

  const openable = openableKnowledgePath(doc?.files_path);
  // The Files pane reaches the file only when the Agent names it relative to the browse root.
  const editable = !!doc && !openable;
  return (
    <div className="igen-memo-doc" data-scope={scope}>
      <div className="igen-memo-head">
        <span className="igen-label">{tr(scope === "family" ? "imggen.memo_family" : "imggen.memo_model", { key: docKey })}</span>
        {openable ? (
          <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={() => openFileMode(openable, "edit")}>
            {tr("imggen.memo_open_files")}
          </button>
        ) : (
          doc?.path && <code className="igen-memo-path">{doc.path}</code>
        )}
        {editable && !editing && (
          <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" disabled={busy} onClick={() => void startEdit()}>
            {tr("imggen.memo_edit")}
          </button>
        )}
      </div>
      {editable && !editing && <p className="igen-hint">{tr("imggen.memo_edit_hint")}</p>}
      {err && <p className="igen-err">{err}</p>}
      {doc?.summary_truncated && <p className="igen-warn">{tr("imggen.memo_summary_long")}</p>}
      {editing ? (
        <div className="igen-memo-edit">
          {SECTIONS.map((sec) => (
            <label className="igen-memo-sec" key={sec}>
              <span className="igen-memo-cap">{tr(`imggen.memo_${sec}` as "imggen.memo_summary")}</span>
              <textarea
                className="igen-memo-input"
                data-section={sec}
                rows={sec === "records" ? 5 : 3}
                value={editing[sec]}
                onChange={(e) => setEditing({ ...editing, [sec]: e.target.value })}
              />
            </label>
          ))}
          <div className="igen-memo-actions">
            <button type="button" className="ui-btn ui-btn-sm ui-btn-primary" disabled={busy} onClick={() => void save()}>
              {tr("imggen.memo_save")}
            </button>
            <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" disabled={busy} onClick={() => setEditing(null)}>
              {tr("imggen.memo_cancel")}
            </button>
          </div>
        </div>
      ) : (
        doc &&
        SECTIONS.map((sec) => (
          <div className="igen-memo-sec" key={sec}>
            <span className="igen-memo-cap">{tr(`imggen.memo_${sec}` as "imggen.memo_summary")}</span>
            {doc[sec]?.trim() ? <pre className="igen-memo-text">{doc[sec]}</pre> : <span className="igen-hint">—</span>}
          </div>
        ))
      )}
      {!editing && (
        <div className="igen-memo-add">
          <textarea
            className="igen-memo-input"
            rows={2}
            value={note}
            placeholder={tr("imggen.memo_add_ph")}
            onChange={(e) => setNote(e.target.value)}
          />
          <button type="button" className="ui-btn ui-btn-sm" disabled={busy || !note.trim()} onClick={() => void add()}>
            {tr("imggen.memo_add")}
          </button>
        </div>
      )}
    </div>
  );
}
