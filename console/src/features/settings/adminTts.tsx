import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { setTenantDict } from "../chat/ttsDict.ts";

export function TtsAdminView() {
  const tr = useT();
  const [data, setData] = useState<any | null>(null); // { managed, enabled, engine, polly, dict }
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  // テナント共通の読み仮名辞書（全ユーザーの読み上げに適用。ユーザー辞書が同表記を上書き）。
  // dict=編集中の値（null=未ロード）、savedDict=サーバ側の値（dirty 判定用）。
  const [dict, setDict] = useState<string | null>(null);
  const [savedDict, setSavedDict] = useState("");
  const [dictBusy, setDictBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/tts");
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      setData(d);
      const dv = typeof d.dict === "string" ? d.dict : "";
      setSavedDict(dv);
      setDict((cur) => (cur === null ? dv : cur)); // 編集中の入力はポーリングで潰さない
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [tr]);
  useEffect(() => {
    load();
  }, [load]);
  // 有効なのに未 ready（ECS 起動中）の間は自動更新して readiness を追う。エンジン不在で
  // トグルを固定している間も追う — エンジンが現れたら固定が外れなければならない。
  // 追う必要が無いのは「いま使える」か「管理下で意図して停止している」かのどちらか。
  useEffect(() => {
    if (!data || data.engine?.ready) return;
    if (!data.enabled && data.managed) return;
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [data, load]);

  const setEnabled = async (enabled: boolean) => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/tts", "PUT", { enabled });
      if (d?.error) setErr(errText(d.error));
      else setData(d);
    } finally {
      setBusy(false);
    }
  };

  const saveDict = async () => {
    if (dict === null) return;
    setDictBusy(true);
    try {
      const d = await apiJSON("api/admin/tts/dict", "PUT", { dict });
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      setData(d);
      setSavedDict(dict);
      setTenantDict(dict); // 自分のブラウザの読み上げにも即反映（他ユーザーは次回ロードから）
    } finally {
      setDictBusy(false);
    }
  };

  const engine = data?.engine || {};
  // エンジンが「無い」: ECS 管理下でもなく（＝この画面から起動する手段が無い）、URL にも
  // 到達できない。有効にしたところで VOICEVOX へは一切流れず、auto は日本語まで Polly に
  // 落ちるので、実効状態は無効そのもの。設定値を書き換えず、表示と操作だけを無効で固定する
  // （エンジンが現れれば上のポーリングで固定が外れ、記録されていた意図がそのまま復活する）。
  const noEngine = !!data && !data.managed && !engine.ready;
  const enabled = !!data?.enabled && !noEngine;
  const engineLabel = !data
    ? "…"
    : engine.ready
      ? tr("admin.tts_running")
      : engine.state === "starting"
        ? tr("admin.tts_starting")
        : engine.state === "running"
          ? tr("admin.tts_running_waiting")
          : enabled && data.managed
            ? tr("admin.tts_stopped")
            : tr("admin.tts_stopped_or_off");

  return (
    <div className="admin-stage">
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.tts_engine_label")}</span>
          <span className="seg sm">
            <button
              type="button"
              className={"seg-btn" + (enabled ? " active" : "")}
              disabled={busy || data === null || noEngine}
              onClick={() => setEnabled(true)}
            >
              {tr("admin.enable")}
            </button>
            <button
              type="button"
              className={"seg-btn" + (!enabled ? " active" : "")}
              disabled={busy || data === null || noEngine}
              onClick={() => setEnabled(false)}
            >
              {tr("admin.disable")}
            </button>
          </span>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        {data && (
          <>
            <p className={engine.ready ? "muted" : enabled ? "form-err" : "muted"}>
              {tr("admin.tts_engine_prefix")}{engineLabel}
              {data.managed ? tr("admin.tts_managed") : tr("admin.tts_external")}
              {tr("admin.tts_polly_sep")}{data.polly?.ready ? tr("admin.tts_polly_ready") : tr("admin.tts_polly_unset")}
            </p>
            {enabled && !engine.ready && data.managed && (
              <p className="muted">{tr("admin.tts_starting_note")}</p>
            )}
            {noEngine && <p className="muted">{tr("admin.tts_no_engine")}</p>}
            {engine.error && <p className="form-err">{engine.error}</p>}
          </>
        )}
        {err && <p className="form-err">{err}</p>}
        <p className="muted">{tr("admin.tts_disable_note")}</p>
      </section>
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.tts_dict_title")}</span>
          <button
            type="button"
            className="btn primary"
            disabled={dictBusy || dict === null || dict === savedDict}
            onClick={saveDict}
          >
            {dictBusy ? tr("admin.saving") : tr("common.save")}
          </button>
        </div>
        <textarea
          className="ds-userdict"
          value={dict ?? ""}
          onChange={(e) => setDict(e.target.value)}
          rows={8}
          spellCheck={false}
          disabled={dict === null}
          placeholder={tr("admin.tts_dict_ph")}
        />
        <p className="muted">{tr("admin.tts_dict_note")}</p>
      </section>
    </div>
  );
}

// --- テナント一覧（ルートの入口）-------------------------------------------
// カードを開くとレールごとそのテナントの面に入る（ドリルダウンの 1 段目）。
