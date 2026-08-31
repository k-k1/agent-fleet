// InstructionsTab — ユーザー指示（docs/log/60 / ADR 0042）。
//
// フリート方針（イメージ焼き込み・オペレーター所有）とプロジェクト指示（リポジトリに
// コミットされる）の**間**の層。ここに書いた文章 1 本が、対応する全 kind の新しい
// セッションへ配られる。実体は Agent 側にあり（~/.config/agent-fleet/user-notes.md）、
// この画面は REST を叩くだけ。
//
// 画面の作りで意識していること:
//   ① **「書いた」と「効いている」を別に出す。** 保存できても、その kind の設定が
//      読めなければ効かない（opencode の .jsonc にコメントがある場合など）。行ごとに
//      配り方・実際のパス・適用状態を出し、失敗は理由コードで見せる。
//   ② **未対応 kind も行として出す。** 消すと「対応漏れ」に見え、同じ質問が繰り返される。
//      cursor はローカルに user 層が無い（Cursor アカウント側）ので理由付きで確定表示。
//   ③ **「新しいセッションから有効」**。走っているセッションには遡及しない。
import { useCallback, useState } from "react";
import {
  api,
  apiJSON,
  errText,
  isTransientErr,
} from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { agentOf } from "../../agents/registry.ts";
import { OnOff, Row } from "./controls.tsx";
import { useT, tMaybe } from "../../lib/i18n/index.ts";

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
  // 覗いている「実際に読まれるファイル」。null = 閉じている。
  const [peek, setPeek] = useState<{
    kind: string;
    path: string;
    content: string;
  } | null>(null);

  const load = useCallback(
    async (signal: AbortSignal) => {
      if (!running) return true; // 停止中は叩かない（起動後に deps で再実行）
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
        {/* 上限の根拠は truncation ではなく費用。毎セッションに乗る固定費であることを言う。 */}
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
