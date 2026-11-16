import { Fragment, useEffect, useRef } from "react";
import type { ReactNode } from "react";
import { Button } from "../../../ui/Button.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { ConnChecking, type ConnPhase } from "../parts/providerCard.tsx";
import { useConnections } from "../parts/useConnections.ts";
import { useSettingsTarget } from "../parts/useSettingsTarget.ts";
import { ClaudeCard } from "./ClaudeCard.tsx";
import { CodexCard } from "./CodexCard.tsx";
import { CursorCard } from "./CursorCard.tsx";
import { CopilotCard } from "./CopilotCard.tsx";
import { KiroCard } from "./KiroCard.tsx";
import { AgyCard } from "./AgyCard.tsx";
import { OpencodeCard } from "./OpencodeCard.tsx";
import { LcppCard } from "./LcppCard.tsx";
import { MuseCard } from "./MuseCard.tsx";
import { OwnSettingsLine, UnusableLine } from "./MemberLines.tsx";
import type { ProviderConn } from "../../../types/session.ts";

/** The destination groups of ADR 0138 D16, in its order: who is paid, not which harness uses it.
 *  Each names the GET /connections entries whose cards it holds. */
export const CREDENTIAL_GROUPS: { key: string; label: string; kinds: string[] }[] = [
  { key: "anthropic", label: "cred.group_anthropic", kinds: ["claude"] },
  { key: "openai", label: "cred.group_openai", kinds: ["codex"] },
  // opencode's card still holds every key it stores (Anthropic, OpenAI, Gemini, other providers)
  // until those split into their destinations' groups.
  { key: "opencode", label: "cred.group_opencode", kinds: ["opencode"] },
  { key: "google", label: "cred.group_google", kinds: ["agy"] },
  { key: "meta", label: "cred.group_meta", kinds: ["muse"] },
  { key: "github", label: "cred.group_github_cursor_kiro", kinds: ["copilot", "cursor", "kiro"] },
  { key: "selfhosted", label: "cred.group_selfhosted", kinds: ["lcpp"] },
];

// One card per GET /connections entry. A map rather than a switch, so the kind names stay data.
const CARDS: Record<string, (st: ProviderConn | undefined, reload: () => void) => ReactNode> = {
  claude: (st, reload) => <ClaudeCard running st={st} reload={reload} />,
  codex: (st, reload) => <CodexCard running st={st} reload={reload} />,
  cursor: (st, reload) => <CursorCard running st={st} reload={reload} />,
  copilot: (st) => <CopilotCard running st={st} />,
  kiro: (st, reload) => <KiroCard running st={st} reload={reload} />,
  agy: (st, reload) => <AgyCard running st={st} reload={reload} />,
  opencode: (st, reload) => <OpencodeCard running st={st} reload={reload} />,
  lcpp: (st, reload) => <LcppCard running st={st} reload={reload} />,
  muse: (st, reload) => <MuseCard running st={st} reload={reload} />,
};

/**
 * CredentialsTab is Settings > Connections > Model credentials (ADR 0138 D16): every sign-in, key
 * and endpoint a model runs on, grouped by destination. The harnesses' behaviour is on the Agents
 * page.
 *
 * A credential the policy forbids is not listed (D10a, D14a): GET /connections has no entry for a
 * kind the policy forbids altogether, or one the member switched off, so a card is drawn only for
 * an entry the Agent sent. With no answer to go by (stopped, still checking, failed) no card is
 * drawn at all, rather than every card.
 */
export function CredentialsTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";
  const { conns, reload, failed } = useConnections();
  useEffect(() => {
    if (running) reload();
  }, [running, reload]);

  const checking = running && !conns;
  const connFailed = running && !!conns && failed;
  const phase: ConnPhase = checking ? "checking" : connFailed ? "failed" : "ok";
  const known = running && phase === "ok" && !!conns;

  // A deep link (the transcript's Re-authenticate button) names the card to land on; scrolling
  // waits for the snapshot, whose cards are what the target is among.
  const rootRef = useRef<HTMLDivElement>(null);
  useSettingsTarget(rootRef, phase !== "checking");

  const card = (kind: string): ReactNode => {
    const st = conns?.[kind as keyof typeof conns] as ProviderConn | undefined;
    const draw = CARDS[kind];
    return draw ? <Fragment key={kind}>{draw(st, reload)}</Fragment> : null;
  };

  return (
    <div className="conns" ref={rootRef}>
      {!running && (
        <div className="agents-ws-hint">
          <p className="muted ds-note">{tr("cred.ws_required_hint")}</p>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
          </Button>
        </div>
      )}
      <UnusableLine running={running} />
      <OwnSettingsLine running={running} />
      <ConnChecking.Provider value={phase}>
        {checking && <p className="muted ds-note">{tr("cred.checking")}</p>}
        {connFailed && (
          <div className="agents-ws-hint">
            <p className="muted ds-note">{tr("conn.check_failed_note")}</p>
            <Button icon="refresh" onClick={reload}>
              {tr("conn.retry")}
            </Button>
          </div>
        )}
        {known && (
          <>
            <p className="muted ds-note">{tr("agents.note_apply")}</p>
            {CREDENTIAL_GROUPS.map((g) => {
              const present = g.kinds.filter((k) => conns && k in conns && conns[k as keyof typeof conns]);
              if (present.length === 0) return null;
              return (
                <section key={g.key} className="ds-group cred-group" data-cred-group={g.key}>
                  <h4 className="ds-title">{tr(g.label as Parameters<typeof tr>[0])}</h4>
                  {present.map(card)}
                </section>
              );
            })}
          </>
        )}
      </ConnChecking.Provider>
    </div>
  );
}
