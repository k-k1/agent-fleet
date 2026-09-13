// ImageProps — a picture's properties, in the shared lightbox's bar (ADR 0081 decision 3).
//
// One record, one reader, one surface: the gallery, the mirror's file card and the studio
// all meet at the lightbox, so this panel is the only place seed / model / prompt are shown
// and copied. ADR 0080 implements nothing for it on purpose.
//
// Three things it must not do, each of them measured:
//   - never read from the thumbnail route: `fs/download?thumb=` is a JPEG re-encode and
//     drops the PNG text chunk (a chunk that does come back is the under-128 KiB
//     original-passthrough case, an accident, not a contract);
//   - never fetch a folder's worth of originals — that is the bandwidth ADR 0080 decision 4
//     refused. This component fetches ONCE, when it is opened;
//   - never fill blanks for `source: "none"`. A vendor-route PNG carries no chunk at all,
//     and a table of empty rows reads as a bug rather than as "this cannot be recovered".
import { useEffect, useState } from "react";
import { errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { toast } from "../../ui/toast.ts";
import { Icon } from "../../ui/Icon.tsx";
import { imageProperties, type ImageProperties } from "../imagegen/api.ts";
import { currentDraft, openImagegen } from "../imagegen/open.ts";
import { draftFromProperties } from "../imagegen/draft.ts";

interface Row {
  key: string;
  label: string;
  value: string;
}

/** Only the fields the Agent actually resolved. An absent one is left out, not blanked. */
function rowsOf(p: ImageProperties, tr: (k: string, v?: Record<string, unknown>) => string): Row[] {
  const out: Row[] = [];
  const add = (key: string, label: string, value: string | number | undefined | null) => {
    if (value === undefined || value === null || value === "") return;
    out.push({ key, label, value: String(value) });
  };
  add("model", tr("imggen.props_model"), p.model);
  add("family", tr("imggen.props_family"), p.family);
  add("seed", tr("imggen.props_seed"), p.seed);
  add("size", tr("imggen.props_size"), p.size);
  add("steps", tr("imggen.props_steps"), p.steps);
  add("cfg", tr("imggen.props_cfg"), p.cfg);
  add("sampler", tr("imggen.props_sampler"), p.sampler);
  add("scheduler", tr("imggen.props_scheduler"), p.scheduler);
  if (p.loras?.length) {
    add("loras", tr("imggen.props_loras"), p.loras.map((l) => `${l.name}${l.weight != null ? ` @${l.weight}` : ""}`).join(", "));
  }
  add("prompt", tr("imggen.props_prompt"), p.prompt);
  add("negative", tr("imggen.props_negative"), p.negative_prompt);
  return out;
}

export function ImageProps({ path }: { path: string }) {
  const tr = useT();
  const [props, setProps] = useState<ImageProperties | null>(null);
  const [err, setErr] = useState("");

  useEffect(() => {
    let alive = true;
    setProps(null);
    setErr("");
    imageProperties(path)
      .then((p) => {
        if (!alive) return;
        if (!p || p.error) {
          setErr(errText(p?.error) || tr("imggen.props_failed"));
          return;
        }
        setProps(p);
      })
      .catch(() => alive && setErr(tr("imggen.props_failed")));
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);

  const copy = (text: string) => {
    navigator.clipboard
      ?.writeText(text)
      .then(() => toast(tr("imggen.props_copied"), { kind: "info" }))
      .catch(() => toast(tr("imggen.props_copy_failed"), { kind: "error" }));
  };

  if (err) return <div className="imgprops imgprops-msg">{err}</div>;
  if (!props) return <div className="imgprops imgprops-msg">{tr("imggen.props_loading")}</div>;
  if (props.source === "none") return <div className="imgprops imgprops-msg">{tr("imggen.props_none")}</div>;

  const rows = rowsOf(props, tr as (k: string, v?: Record<string, unknown>) => string);
  return (
    <div className="imgprops">
      <div className="imgprops-src">
        {tr("imggen.props_source")}:{" "}
        {tr(props.source === "sidecar" ? "imggen.props_source_sidecar" : "imggen.props_source_png")}
      </div>
      <dl className="imgprops-rows">
        {rows.map((r) => (
          <div className="imgprops-row" key={r.key}>
            <dt>{r.label}</dt>
            <dd title={r.value}>{r.value}</dd>
            <button
              type="button"
              className="imgprops-copy"
              title={tr("imggen.props_copy", { field: r.label })}
              aria-label={tr("imggen.props_copy", { field: r.label })}
              onClick={() => copy(r.value)}
            >
              <Icon name="copy" />
            </button>
          </div>
        ))}
      </dl>
      <div className="imgprops-actions">
        <button type="button" className="ui-btn ui-btn-ghost" onClick={() => copy(JSON.stringify(props, null, 2))}>
          {tr("imggen.props_copy_all")}
        </button>
        <button
          type="button"
          className="ui-btn ui-btn-ghost"
          // Lays the recovered fields over the draft as it stands, so what the picture did
          // not carry keeps whatever the person had typed (draft.ts documents the rule).
          onClick={() => openImagegen({ draft: draftFromProperties(currentDraft(), props) })}
        >
          {tr("imggen.props_open_gen")}
        </button>
      </div>
    </div>
  );
}
