import { useEffect, useState } from "react";
import {
  sessionSettings,
  sessionSettingsGet,
  type ManagedThreadSettings,
} from "../../core/api/client.ts";
import { EffortPicker, ModelPicker } from "../../ui/ModelPicker.tsx";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";

interface ManagedSettingsModalProps {
  session: string;
  kind: string;
  working: boolean;
  onApplied: (settings: ManagedThreadSettings) => void;
  onClose: () => void;
}

export function ManagedSettingsModal({ session, kind, working, onApplied, onClose }: ManagedSettingsModalProps) {
  const toast = useToast();
  const [initial, setInitial] = useState<ManagedThreadSettings | null>(null);
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const [mode, setMode] = useState<"plan" | "normal">("normal");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    void sessionSettingsGet(session)
      .then((r) => {
        if (!alive) return;
        if (r.error) {
          setError(r.error.message || "設定を読み込めませんでした");
          return;
        }
        const current: ManagedThreadSettings = {
          model: r.model || "",
          effort: r.effort || "",
          mode: r.mode || "",
          dynamicModel: r.dynamicModel,
          dynamicEffort: r.dynamicEffort,
          dynamicMode: r.dynamicMode,
        };
        setInitial(current);
        setModel(current.model);
        setEffort(current.effort);
        setMode(current.mode === "plan" ? "plan" : "normal");
      })
      .catch(() => alive && setError("設定を読み込めませんでした（通信エラー）"));
    return () => {
      alive = false;
    };
  }, [session]);

  const changed = !!initial && (
    model !== initial.model || effort !== initial.effort || mode !== (initial.mode || "normal")
  );
  const apply = async () => {
    if (!initial || !changed || busy) return;
    setBusy(true);
    setError("");
    const patch: Parameters<typeof sessionSettings>[1] = {};
    if (model !== initial.model) {
      if (model) patch.model = model;
      else patch.clearModel = true;
    }
    if (effort !== initial.effort) {
      if (effort) patch.effort = effort;
      else patch.clearEffort = true;
    }
    if (mode !== (initial.mode || "normal")) patch.mode = mode;
    const res = await sessionSettings(session, patch);
    setBusy(false);
    if (!res.ok) {
      setError(res.message || "設定を変更できませんでした");
      return;
    }
    const next = res.settings || { ...initial, model, effort, mode };
    onApplied({ ...initial, ...next, model, effort, mode });
    toast("実行設定を更新しました", { kind: "success" });
    onClose();
  };

  return (
    <Modal title={<><Icon name="gear" /> 実行設定</>} onClose={onClose} lockClose={busy}>
      <div className="ui-modal-body managed-settings-body">
        {!initial && !error && <div className="pane-empty"><Icon name="loading" spin /> 設定を読み込み中…</div>}
        {error && <div className="managed-settings-error" role="alert">{error}</div>}
        {initial && (
          <>
            {initial.dynamicModel !== false && (
              <div className="ui-field">
                <span className="ui-field-label">モデル</span>
                <ModelPicker
                  kind={kind}
                  model={model}
                  onChange={(next) => {
                    setModel(next);
                    setEffort("");
                  }}
                />
              </div>
            )}
            {(initial.dynamicEffort !== false || initial.dynamicMode !== false) && (
              <div className="ui-field-row">
                {initial.dynamicEffort !== false && (
                  <div className="ui-field">
                    <span className="ui-field-label">推論 effort</span>
                    <EffortPicker kind={kind} model={model} effort={effort} onChange={setEffort} />
                    <span className="ui-field-hint">既定は標準推論量へ戻します。</span>
                  </div>
                )}
                {initial.dynamicMode !== false && (
                  <div className="ui-field">
                    <span className="ui-field-label">モード</span>
                    <select className="cinput" value={mode} onChange={(e) => setMode(e.target.value === "plan" ? "plan" : "normal")}>
                      <option value="normal">通常（実装）</option>
                      <option value="plan">Plan</option>
                    </select>
                  </div>
                )}
              </div>
            )}
            {working && <div className="ui-field-hint"><Icon name="info" /> 変更は次のターンから反映されます。</div>}
          </>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>キャンセル</Button>
        <Button variant="primary" onClick={() => void apply()} disabled={!changed || busy || !!error}>
          {busy ? "適用中…" : "適用"}
        </Button>
      </footer>
    </Modal>
  );
}
