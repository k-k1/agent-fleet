// useWsSettings — load once, save a patch: the shared half of the CP-owned workspace settings
// (GET/PUT /api/env/ws-settings) for a tab that shows them as plain toggles.
//
// Behaviour is the toolchains tab's own, lifted unchanged: a failed or error-bodied load
// settles to the all-off fallback (so the rows render rather than spin), and a failed save
// toasts `common.save_failed` and leaves the shown value alone. PreviewTab does NOT use this:
// it serialises its writes and guards their tenant scope (see its `serialized`).
import { useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { getWsSettings, isOk, putWsSettings, type WsSettings, type WsSettingsPatch } from "./api.ts";

const OFF: WsSettings = { agentUpdate: false, allowAgentUpdate: false, autoImageRefresh: false };

export function useWsSettings(): { ws: WsSettings | null; save: (patch: WsSettingsPatch) => Promise<void> } {
  const tr = useT();
  const toast = useToast();
  const [ws, setWs] = useState<WsSettings | null>(null);

  useEffect(() => {
    getWsSettings()
      .then((res) => setWs(isOk(res) ? res : OFF))
      .catch(() => setWs(OFF));
  }, []);

  const save = async (patch: WsSettingsPatch) => {
    const res = await putWsSettings(patch);
    if (isOk(res)) setWs(res);
    else toast(tr("common.save_failed"));
  };

  return { ws, save };
}
