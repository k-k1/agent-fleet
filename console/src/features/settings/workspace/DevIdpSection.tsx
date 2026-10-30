// The dev OIDC IdP block of the Preview tab (ADR 0116): the credentials the container gets, the
// one warning that applies to all of them, and copy-ready app configuration for the common
// stacks (templates live in devIdpSnippets.ts). The client secret is deliberately absent: the
// API never returns it, and no snippet writes it as a value.
import { useId, useState } from "react";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { Button } from "../../../ui/Button.tsx";
import { Icon } from "../../../ui/Icon.tsx";
import { openGuidePage } from "../../../lib/guideDocs.ts";
import {
  FRAMEWORKS,
  fileText,
  renderFiles,
  visibleLines,
  type FrameworkId,
  type SnippetCtx,
  type SnippetFile,
  type ValuesMode,
} from "./devIdpSnippets.ts";

// `# …` / `// …` lines read as comments; nothing fancier, so a line never gets mis-coloured.
const isComment = (s: string) => /^\s*(#|\/\/)/.test(s);

export function DevIdpSection({ au, rotate }: { au: any; rotate: () => void }) {
  const tr = useT();
  const toast = useToast();
  // "Copied" only after the clipboard accepted it: the API is absent outside secure contexts
  // and writeText rejects when permission is denied.
  const copy = async (text: string) => {
    try {
      if (!navigator.clipboard) throw new Error("no clipboard");
      await navigator.clipboard.writeText(text);
      toast(tr("env.devidp_copied"));
    } catch {
      toast(tr("common.copy_failed"));
    }
  };
  const issuer: string = au.devIdpIssuer || "";
  const discovery = issuer ? `${issuer}/.well-known/openid-configuration` : "";
  const clientId: string = au.devIdpClientId || "";
  const rows: Array<[string, string]> = [
    [tr("env.devidp_issuer"), issuer],
    [tr("env.devidp_discovery"), discovery],
    [tr("env.devidp_client_id"), clientId],
  ];
  return (
    <>
      <h4 className="ds-title">{tr("env.devidp_title")}</h4>
      <p className="muted ds-note">{tr("env.devidp_note")}</p>
      <div className="dip-creds">
        {rows
          .filter(([, v]) => v)
          .map(([label, v]) => (
            <div key={label} className="dip-row">
              <span className="dip-k">{label}</span>
              <code className="dip-v">{v}</code>
              <Button small className="dip-b" onClick={() => copy(v)} aria-label={`${tr("env.devidp_copy")}: ${label}`}>
                {tr("env.devidp_copy")}
              </Button>
            </div>
          ))}
        <div className="dip-row">
          <span className="dip-k">{tr("env.devidp_rotate_label")}</span>
          <span className="dip-v muted">{tr("env.devidp_secret_value")}</span>
          <Button small className="dip-b" onClick={rotate}>
            {tr("env.devidp_rotate")}
          </Button>
        </div>
      </div>
      <div className="dip-callout" role="note">
        <Icon name="warning" />
        <span>{tr("env.devidp_warn")}</span>
      </div>
      <Examples au={au} issuer={issuer} discovery={discovery} clientId={clientId} copy={copy} />
    </>
  );
}

function Examples({
  au,
  issuer,
  discovery,
  clientId,
  copy,
}: {
  au: any;
  issuer: string;
  discovery: string;
  clientId: string;
  copy: (text: string) => void;
}) {
  const tr = useT();
  const uid = useId();
  const [fwId, setFwId] = useState<FrameworkId>("spring");
  const [variantByFw, setVariantByFw] = useState<Record<string, string>>({});
  const [portPick, setPortPick] = useState<number | null>(null);
  const [preview, setPreview] = useState(true);
  const [values, setValues] = useState<ValuesMode>("env");

  const fw = FRAMEWORKS.find((f) => f.id === fwId)!;
  const variant = fw.variants.find((v) => v.id === variantByFw[fwId]) ?? fw.variants[0];
  const ports: number[] = au.previewPorts || [];
  // The picked port while it is still published; else the stack's usual one if published; else
  // the first published one.
  const port =
    portPick != null && ports.includes(portPick)
      ? portPick
      : ports.includes(variant.defaultPort)
        ? variant.defaultPort
        : (ports[0] ?? variant.defaultPort);
  const ctx: SnippetCtx = {
    issuer,
    discovery,
    clientId,
    port,
    previewUrl: (au.previewUrls || {})[String(port)] || "",
    values,
    preview,
  };
  const files = renderFiles(variant, ctx);
  const fwLabel = (f: (typeof FRAMEWORKS)[number]) => (f.literal ? f.label : tr(f.label as never));
  const variantLabel = (v: (typeof fw.variants)[number]) => tr(v.label as never);
  const committable = values === "env" && variant.commitEnv;

  return (
    <>
      <h4 className="ds-title">{tr("env.devidp_examples_title")}</h4>
      <div className="dip-tabs" role="tablist" aria-label={tr("env.devidp_examples_title")}>
        {FRAMEWORKS.map((f) => (
          <button
            key={f.id}
            type="button"
            role="tab"
            aria-selected={f.id === fwId}
            className={"dip-tab" + (f.id === fwId ? " active" : "")}
            onClick={() => setFwId(f.id)}
          >
            {fwLabel(f)}
          </button>
        ))}
      </div>
      <div className="dip-opts">
        {fw.variants.length > 1 && (
          <div className="dip-opt">
            <span className="dip-opt-k">{tr("env.devidp_opt_variant")}</span>
            {/* Segmented where there is room, a select on a phone: three long labels do not
                fit one line there. Both are in the DOM, CSS shows one. */}
            <div className="seg choice-seg dip-variant-seg">
              {fw.variants.map((v) => (
                <button
                  key={v.id}
                  type="button"
                  className={"seg-btn" + (v.id === variant.id ? " active" : "")}
                  onClick={() => setVariantByFw((m) => ({ ...m, [fwId]: v.id }))}
                >
                  {variantLabel(v)}
                </button>
              ))}
            </div>
            <select
              className="dip-variant-select"
              aria-label={tr("env.devidp_opt_variant")}
              value={variant.id}
              onChange={(e) => setVariantByFw((m) => ({ ...m, [fwId]: e.target.value }))}
            >
              {fw.variants.map((v) => (
                <option key={v.id} value={v.id}>
                  {variantLabel(v)}
                </option>
              ))}
            </select>
          </div>
        )}
        {ports.length > 0 && (
          <label className="dip-opt">
            <span className="dip-opt-k">{tr("env.devidp_opt_port")}</span>
            <select value={port} onChange={(e) => setPortPick(Number(e.target.value))}>
              {ports.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </label>
        )}
        <label className="dip-opt dip-check">
          <input type="checkbox" checked={preview} onChange={(e) => setPreview(e.target.checked)} />
          <span>{tr("env.devidp_opt_preview")}</span>
        </label>
        <div className="dip-opt" role="group" aria-labelledby={`${uid}-values`}>
          <span className="dip-opt-k" id={`${uid}-values`}>
            {tr("env.devidp_opt_values")}
          </span>
          <div className="seg choice-seg">
            {(["env", "fill"] as const).map((m) => (
              <button
                key={m}
                type="button"
                className={"seg-btn" + (values === m ? " active" : "")}
                onClick={() => setValues(m)}
              >
                {tr(m === "env" ? "env.devidp_values_env" : "env.devidp_values_fill")}
              </button>
            ))}
          </div>
        </div>
      </div>
      {files.map((f) => (
        <CodeBox key={f.file} file={f} preview={preview} copy={copy} />
      ))}
      <ul className="dip-notes">
        <li>
          <Icon name={committable ? "check" : "warning"} />
          <span>{tr(committable ? "env.devidp_note_commit_yes" : "env.devidp_note_commit_no")}</span>
        </li>
        {variant.sharedWithProd && (
          <li>
            <Icon name="warning" />
            <span>{tr("env.devidp_note_shared")}</span>
          </li>
        )}
        <li>
          <Icon name="shield" />
          <span>{tr("env.devidp_note_prod")}</span>
        </li>
        <li>
          <Icon name="device-desktop" />
          <span>
            {tr("env.devidp_note_local")}{" "}
            <button type="button" className="dip-link" onClick={() => openGuidePage("member/14-preview-browser")}>
              {tr("env.devidp_note_local_link")}
            </button>
          </span>
        </li>
      </ul>
    </>
  );
}

function CodeBox({ file, preview, copy }: { file: SnippetFile; preview: boolean; copy: (text: string) => void }) {
  const tr = useT();
  const lines = visibleLines(file, preview);
  return (
    <div className="dip-code">
      <div className="dip-codebar">
        <span className="dip-file">{file.file}</span>
        {file.activate && <span className="dip-act">{tr(file.activate.key as never, file.activate.params)}</span>}
        <Button
          small
          className="dip-copy"
          onClick={() => copy(fileText(file, preview))}
          aria-label={`${tr("env.devidp_copy")}: ${file.file}`}
        >
          {tr("env.devidp_copy")}
        </Button>
      </div>
      <pre className="dip-pre">
        <code>
          {lines.map((l, i) => (
            <span
              key={i}
              className={"dip-line" + (l.when === "preview" ? " dip-mark" : "") + (isComment(l.text) ? " dip-cmt" : "")}
            >
              {l.text}
              {"\n"}
            </span>
          ))}
        </code>
      </pre>
    </div>
  );
}
