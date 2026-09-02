import { useEffect } from "react";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useConnections } from "../parts/useConnections.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { DiscordCard } from "./DiscordCard.tsx";
import { SlackCard } from "./SlackCard.tsx";

// ChatTab (チャット連携) — the chat-bridge CONNECTIONS (Discord / Slack, docs/log/37), split
// out of 運用・監視 into their own 接続 tab: these are notification destinations, not
// monitoring providers. Each card separates CONNECT (token → verify → pick channel →
// 接続, minimal) from the detail SETTINGS (threads / mention / receive / mirror / events /
// full-text), which live in a collapsible 通知設定 disclosure that AUTO-SAVES each toggle
// (like the agent 動作設定) — no 編集/保存 button. The master 通知 ON/OFF lives in
// 個人設定 › 通知. Credentials are stored container-side (encrypted) and injected into the
// MCP/bridge at spawn; they never reach the CP.
export function ChatTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const startWs = useWorkspaceStore((s) => s.start);
  const { conns, reload } = useConnections();
  useEffect(() => {
    if (running) reload();
  }, [running, reload]);

  return (
    <div className="conns">
      {!running ? (
        <EmptyState icon="debug-disconnect" title={tr("ops.ws_required_title")} hint={tr("ops.ws_required_hint")}>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
          </Button>
        </EmptyState>
      ) : !conns ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : (
        <>
          <p className="muted ds-note">{tr("chat.intro")}</p>
          <DiscordCard st={conns.discord} reload={reload} />
          <SlackCard st={conns.slack} reload={reload} />
        </>
      )}
    </div>
  );
}
