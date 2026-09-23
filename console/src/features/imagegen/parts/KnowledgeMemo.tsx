// KnowledgeMemo — the workspace's notes on a family and on a model (ADR 0100 decision 12),
// under the family card. Four sections; "records" is append-only and the one this pane writes
// (the same route add_image_knowledge uses). The other three are written by the agent's Edit and
// by the member in the Files pane — which only reaches the file when the browse root is home,
// i.e. when the Agent reports the path relative to it.
//
// Read on demand (the memo is folded by default), never on the studio's 2 s poll: the summary
// already rides every get_image_studio, and the pane has no reason to pay for it per tick.
import { useCallback, useEffect, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { openFileMode } from "../../viewer/openFile.ts";
import { addImagegenKnowledge, imagegenKnowledge, type Knowledge, type KnowledgeScope } from "../api.ts";

const SECTIONS = ["summary", "settings", "prompts", "records"] as const;

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

  const openable = openableKnowledgePath(doc?.path);
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
      </div>
      {err && <p className="igen-err">{err}</p>}
      {doc?.summary_truncated && <p className="igen-warn">{tr("imggen.memo_summary_long")}</p>}
      {doc &&
        SECTIONS.map((sec) => (
          <div className="igen-memo-sec" key={sec}>
            <span className="igen-memo-cap">{tr(`imggen.memo_${sec}` as "imggen.memo_summary")}</span>
            {doc[sec]?.trim() ? <pre className="igen-memo-text">{doc[sec]}</pre> : <span className="igen-hint">—</span>}
          </div>
        ))}
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
    </div>
  );
}
