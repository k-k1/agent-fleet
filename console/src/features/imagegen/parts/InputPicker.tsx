// InputPicker — reference images for `edit` / `inpaint` (ADR 0081 decision 9).
//
// Paths, never bytes: `inputs[]` on the request is a list of browse-root paths, and the
// pixels have to be on the workspace disk for the Agent to send them to the box. A dropped
// file therefore takes the long way round — the existing `POST /fs/upload` into
// `generated/console/inputs/`, then the path it landed at. Picking from the gallery is P1.
import { useRef, useState } from "react";
import type { DragEvent as RDragEvent } from "react";
import { uploadFiles } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { IconButton } from "../../../ui/Button.tsx";

/** Where a dropped reference lands. A sibling of the pictures, so the gallery lists it and
 *  the folder is one the member already knows the name of. */
export const INPUT_DIR = "generated/console/inputs";

export function InputPicker({ paths, onChange }: { paths: string[]; onChange: (p: string[]) => void }) {
  const tr = useT();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [over, setOver] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const add = (p: string) => {
    const path = p.trim().replace(/^\/+/, "");
    if (!path || paths.includes(path)) return;
    onChange([...paths, path]);
  };

  const upload = async (files: FileList | File[]) => {
    const list = [...files];
    if (!list.length) return;
    setBusy(true);
    setErr("");
    try {
      const r = await uploadFiles(INPUT_DIR, list);
      // uploadFiles folds the HTTP status onto the body, so a 409 or a 5xx arrives as data
      // rather than a throw. Anything that is not a 2xx keeps the list untouched.
      if (r.status < 200 || r.status >= 300) {
        setErr(tr("imggen.input_upload_failed"));
        return;
      }
      onChange([...paths, ...list.map((f) => `${INPUT_DIR}/${f.name}`).filter((p) => !paths.includes(p))]);
    } catch {
      setErr(tr("imggen.input_upload_failed"));
    } finally {
      setBusy(false);
    }
  };

  const onDrop = (e: RDragEvent) => {
    e.preventDefault();
    setOver(false);
    if (e.dataTransfer?.files?.length) void upload(e.dataTransfer.files);
  };

  return (
    <div className="igen-inputs">
      <span className="igen-label">{tr("imggen.inputs")}</span>
      <ul className="igen-input-list">
        {paths.map((p) => (
          <li key={p}>
            <span title={p}>{p}</span>
            <IconButton
              icon="close"
              label={tr("imggen.input_remove", { path: p })}
              onClick={() => onChange(paths.filter((x) => x !== p))}
            />
          </li>
        ))}
      </ul>
      <div className="igen-row">
        <input
          className="ds-input"
          value={typed}
          placeholder={tr("imggen.input_ph")}
          onChange={(e) => setTyped(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== "Enter") return;
            e.preventDefault();
            add(typed);
            setTyped("");
          }}
        />
        <button
          type="button"
          className="ui-btn"
          onClick={() => {
            add(typed);
            setTyped("");
          }}
        >
          {tr("imggen.input_add")}
        </button>
      </div>
      <div
        className={"igen-drop" + (over ? " over" : "")}
        onDragOver={(e) => {
          e.preventDefault();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={onDrop}
        onClick={() => fileRef.current?.click()}
        role="presentation"
      >
        <Icon name={busy ? "loading" : "cloud-upload"} spin={busy} />
        {busy ? tr("imggen.input_uploading") : tr("imggen.input_drop")}
        <input
          ref={fileRef}
          type="file"
          multiple
          hidden
          onChange={(e) => e.target.files && void upload(e.target.files)}
        />
      </div>
      {err && <span className="igen-err">{err}</span>}
      <span className="igen-hint">{tr("imggen.input_from_gallery")}</span>
    </div>
  );
}
