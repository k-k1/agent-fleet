// InstructionsTab — user instructions (docs/log/60 / ADR 0042).
//
// The layer between the fleet policy (baked into the image, owned by the operator) and the
// project instructions (committed to the repository). The single text written here is
// delivered to every new session of every supported kind. The real state lives on the Agent
// side (~/.config/agent-fleet/user-notes.md); this screen only calls REST.
//
// What the screen deliberately does:
//   1. Show "written" and "in effect" as separate things. A save can succeed and still have no
//      effect when that kind's config cannot be read (for example an opencode .jsonc with
//      comments in it), so each row shows the delivery method, the real path and the applied
//      state, and a failure is shown with its reason code.
//   2. List unsupported kinds as rows too. Removing them reads as a gap in coverage and
//      invites the same question again; cursor has no local user layer (it lives in the Cursor
//      account), so it is listed permanently with that reason.
//   3. Say that it only takes effect from the next session: running sessions are not
//      retroactively changed.
import { useCallback, useState } from "react";
import {
  api,
  apiJSON,
  errText,
  isTransientErr,
} from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { agentOf } from "../../../agents/registry.ts";
import { OnOff, Row } from "../parts/controls.tsx";
import { useT, tMaybe } from "../../../lib/i18n/index.ts";

interface Target {
  kind: string;
  supported: boolean;
  reason?: string;
  delivery?: string;
  path?: string;
  on: boolean;
  applied: boolean;
  error?: string;
}
interface Payload {
  text: string;
  bytes: number;
  max_bytes: number;
  enabled: boolean;
  path: string;
  targets: Target[];
  fleet_bytes: number;
}

const utf8Bytes = (s: string) => new TextEncoder().encode(s).byteLength;

export function InstructionsTab() {
  const tr = useT();
  const toast = useToast();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";

  const [data, setData] = useState<Payload | null>(null);
  const [err, setErr] = useState("");
  const [draft, setDraft] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // The actually-read file currently being peeked at. null = closed.
  const [peek, setPeek] = useState<{
    kind: string;
    path: string;
    content: string;
  } | null>(null);

  const load = useCallback(
    async (signal: AbortSignal) => {
      if (!running) return true; // don't call while stopped; deps re-run this once it starts
      const r = await api("api/user-notes");
      if (signal.aborted) return true;
      if (isTransientErr(r)) return false;
      if (r?.error) {
        setErr(errText(r.error));
        return true;
      }
      setErr("");
      setData(r);
      return true;
    },
    [running],
  );
  useRetryLoad(load, [running]);

  const text = draft ?? data?.text ?? "";
  const bytes = utf8Bytes(text);
  const max = data?.max_bytes ?? 8192;
  const over = bytes > max;
  const dirty = draft !== null && draft !== (data?.text ?? "");

  const put = async (body: Record<string, unknown>, okMsg?: string) => {
    setBusy(true);
    try {
      const res = await apiJSON("api/user-notes", "PUT", body);
      if (res?.error) {
        toast(errText(res.error));
        return false;
      }
      setData(res);
      if (okMsg) toast(okMsg);
      return true;
    } finally {
      setBusy(false);
    }
  };

  const save = async () => {
    if (over) return;
    if (await put({ text }, tr("instr.saved"))) setDraft(null);
  };

  const openPeek = async (kind: string) => {
    const res = await api(
      "api/user-notes/preview?kind=" + encodeURIComponent(kind),
    );
    if (res?.error) {
      toast(errText(res.error));
      return;
    }
    setPeek({ kind, path: res.path, content: res.content });
  };

  if (!running) {
    return (
      <div className="instr-tab">
        <EmptyState
          icon="book"
          title={tr("instr.ws_required_title")}
          hint={tr("instr.ws_required_hint")}
        >
          <Button
            icon="play"
            disabled={wsStartBusy(wsState)}
            onClick={() => void startWs()}
          >
            {wsStartBusy(wsState)
              ? tr("common.starting")
              : tr("instr.start_ws")}
          </Button>
        </EmptyState>
      </div>
    );
  }

  return (
    <div className="instr-tab">
      <p className="ds-hint">{tr("instr.intro")}</p>
      {err && <div className="ds-error">{err}</div>}

      <Row label={tr("instr.enabled")}>
        <OnOff
          value={data?.enabled ?? true}
          onChange={(v) => void put({ enabled: v })}
        />
      </Row>

      <label className="instr-editor-label" htmlFor="instr-body">
        {tr("instr.body_label")}
      </label>
      <textarea
        id="instr-body"
        className="instr-editor"
        spellCheck={false}
        rows={12}
        value={text}
        placeholder={tr("instr.placeholder")}
        onChange={(e) => setDraft(e.target.value)}
      />
      <div className="instr-meta">
        <span className={over ? "instr-over" : ""}>
          {tr("instr.bytes", { bytes: String(bytes), max: String(max) })}
        </span>
        {/* The limit exists for cost, not truncation: say that it is a fixed cost added to
            every session. */}
        <span className="ds-hint">{tr("instr.cost_hint")}</span>
        <span className="instr-actions">
          <Button disabled={!dirty || over || busy} onClick={() => void save()}>
            {tr("common.save")}
          </Button>
          <Button
            variant="ghost"
            disabled={!dirty}
            onClick={() => setDraft(null)}
          >
            {tr("common.cancel")}
          </Button>
        </span>
      </div>
      {over && <div className="ds-error">{tr("instr.too_large")}</div>}

      <h4 className="ds-subhead">{tr("instr.targets_head")}</h4>
      <p className="ds-hint">{tr("instr.new_sessions_only")}</p>
      <table className="instr-targets">
        <tbody>
          {(data?.targets ?? []).map((t) => (
            <tr key={t.kind} className={t.supported ? "" : "instr-unsupported"}>
              <td className="instr-kind">
                <span
                  className={`codicon codicon-${agentOf(t.kind).icon}`}
                  aria-hidden="true"
                />
                {agentOf(t.kind).label}
              </td>
              <td>
                {t.supported ? (
                  <OnOff
                    value={t.on}
                    onChange={(v) => void put({ targets: { [t.kind]: v } })}
                  />
                ) : (
                  <span className="instr-badge">
                    {tMaybe(`instr.reason_${t.reason}`) ?? t.reason}
                  </span>
                )}
              </td>
              <td className="instr-where">
                {t.supported && (
                  <>
                    <span className="instr-delivery">
                      {tMaybe(`instr.delivery_${t.delivery}`) ?? t.delivery}
                    </span>
                    <code title={t.path}>{t.path}</code>
                  </>
                )}
              </td>
              <td className="instr-state">
                {t.supported &&
                  (t.error ? (
                    <span className="instr-fail">
                      {tMaybe(`instr.err_${t.error}`) ?? t.error}
                    </span>
                  ) : t.applied ? (
                    <span className="instr-ok">{tr("instr.in_effect")}</span>
                  ) : (
                    <span className="instr-pending">
                      {tr("instr.not_in_effect")}
                    </span>
                  ))}
              </td>
              <td>
                {t.supported && (
                  <Button variant="ghost" onClick={() => void openPeek(t.kind)}>
                    {tr("instr.peek")}
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      <h4 className="ds-subhead">{tr("instr.fleet_head")}</h4>
      <p className="ds-hint">{tr("instr.fleet_hint")}</p>
      <Button variant="ghost" onClick={() => void openPeek("fleet")}>
        {tr("instr.fleet_view")}
      </Button>

      {peek && (
        <div className="instr-peek">
          <div className="instr-peek-head">
            <code>{peek.path}</code>
            <Button variant="ghost" icon="close" onClick={() => setPeek(null)}>
              {tr("common.close")}
            </Button>
          </div>
          <pre className="instr-peek-body">
            {peek.content || tr("instr.peek_empty")}
          </pre>
        </div>
      )}
    </div>
  );
}
