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

/** InputPicker's props. `max` is the chosen model's own ceiling (ADR 0094 decision 5): the
 *  families differ, so the number is the Agent's word rather than a constant here.
 *
 *  `label` exists because inpaint's MASK is the same control with a different name and a ceiling
 *  of one (ADR 0081 decision 9 — a mask file by path works from day one; painting one needs a
 *  canvas the Console does not have). Two copies of the drop zone would be two places to fix the
 *  next time uploading changes. */
export function InputPicker({
  paths,
  max,
  label,
  onChange,
}: {
  paths: string[];
  max: number;
  label?: string;
  onChange: (p: string[]) => void;
}) {
  const tr = useT();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [over, setOver] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  // Held HERE and not only at the Agent: the Agent's refusal is a 400 the member meets after
  // pressing, and on a queued job it arrives as a failed job rather than as a message next to
  // the field they would have to change. `full` also decides whether the drop zone is offered at
  // all — a zone that accepts a file and then discards it is worse than no zone.
  const room = Math.max(0, max - paths.length);
  const full = room === 0;

  const add = (p: string) => {
    const path = p.trim().replace(/^\/+/, "");
    if (!path || full || paths.includes(path)) return;
    onChange([...paths, path]);
  };

  const upload = async (files: FileList | File[]) => {
    // Truncated before the upload, not after: uploading bytes this request can never name would
    // leave them in the member's own folder with nothing pointing at them.
    const list = [...files].slice(0, room);
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
      <span className="igen-label">
        {label ?? tr("imggen.inputs")}
        {max > 1 && <span className="igen-hint"> {tr("imggen.input_count", { n: paths.length, max })}</span>}
      </span>
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
      {!full && (
        <>
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
              multiple={room > 1}
              hidden
              onChange={(e) => e.target.files && void upload(e.target.files)}
            />
          </div>
        </>
      )}
      {err && <span className="igen-err">{err}</span>}
      <span className="igen-hint">{tr("imggen.input_from_gallery")}</span>
    </div>
  );
}
