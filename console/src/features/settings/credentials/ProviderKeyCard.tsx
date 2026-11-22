import { useState } from "react";
import { errDetail, raw } from "../../../core/api/client.ts";
import { putOpencodeKey } from "./credentialsApi.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { ProviderCard, StatusPill, IssueLink } from "../parts/providerCard.tsx";

/** One provider key a card offers: its env name in the store, the route it opens, and where the
 *  provider issues one. An empty env is the free-form custom entry. */
export interface KeyPreset {
  id: string;
  label: string;
  env: string;
  route: string;
  issueUrl?: string;
}

/**
 * ProviderKeyCard is a provider's API keys on Model credentials (ADR 0138 D16): the Anthropic key
 * under Anthropic, the OpenAI key under OpenAI, and so on. The keys live where they always have,
 * in the store opencode serves from (PUT/DELETE api/connections/opencode, decision 3a: "the store
 * slot unchanged first"), and claude's anthropic-api reads the same one. Only presets whose route
 * the policy allows are offered (D10a); the caller lists only the stored keys it may show.
 */
export function ProviderKeyCard({
  id,
  name,
  presets,
  stored,
  ownsEnv,
  reload,
  restartPending,
}: {
  id: string;
  name: string;
  /** The keys this card can add, already narrowed to allowed routes. */
  presets: KeyPreset[];
  /** Stored key names (never the keys) this card shows. */
  stored: string[];
  /** Whether a custom env name belongs to this card (the "other providers" card takes any). */
  ownsEnv?: (env: string) => boolean;
  reload: () => void;
  /** opencode holds a changed key only after its daemon restarts (offered on its own card). */
  restartPending?: boolean;
}) {
  const tr = useT();
  const toast = useToast();
  const [presetId, setPresetId] = useState(presets[0]?.id ?? "");
  const [customEnv, setCustomEnv] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const active = presets.find((p) => p.id === presetId) ?? presets[0];
  const envName = active ? (active.env || customEnv.trim().toUpperCase()) : "";
  const customOk = !active || active.env !== "" || !ownsEnv || ownsEnv(envName);

  const add = async () => {
    if (!envName || !key.trim() || !customOk) return;
    setBusy(true);
    try {
      const res = await putOpencodeKey(envName, key.trim());
      if (res && res.error) {
        toast(tr("common.save_failed_msg", { msg: errDetail(res.error) }));
        return;
      }
      setKey("");
      setCustomEnv("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (env: string) => {
    await raw(`api/connections/opencode/${encodeURIComponent(env)}`, { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id={id}
      name={name}
      status={<StatusPill on={stored.length > 0}>{stored.length > 0 ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      <div className="p-body">
        {stored.length > 0 && (
          <ul className="oc-keys">
            {stored.map((e) => (
              <li key={e}>
                <code>{e}</code>
                <button className="icon danger" title={tr("common.delete")} onClick={() => void remove(e)}>
                  ✕
                </button>
              </li>
            ))}
          </ul>
        )}
        {restartPending && <p className="ps-note">{tr("cred.key_restart_pending")}</p>}
        {active && (
          <>
            {active.issueUrl && <IssueLink url={active.issueUrl} />}
            <div className="flow">
              {presets.length > 1 && (
                <select className="cinput" value={active.id} onChange={(e) => setPresetId(e.target.value)}>
                  {presets.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.label}
                    </option>
                  ))}
                </select>
              )}
              {active.env === "" && (
                <input
                  className="cinput"
                  placeholder={tr("agents.oc_env_placeholder")}
                  value={customEnv}
                  onChange={(e) => setCustomEnv(e.target.value)}
                />
              )}
              <input
                className="cinput"
                type="password"
                placeholder={envName ? tr("agents.oc_key_value", { env: envName }) : tr("agents.oc_key_fallback")}
                value={key}
                onChange={(e) => setKey(e.target.value)}
              />
              <button disabled={busy || !envName || !key.trim() || !customOk} onClick={() => void add()}>
                {tr("conn.connect")}
              </button>
            </div>
          </>
        )}
      </div>
    </ProviderCard>
  );
}
