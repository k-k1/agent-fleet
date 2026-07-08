// OnboardingCard — ported from the old components/OnboardingCard.tsx (docs/22 P6d).
// First-run getting-started card, overlaid on the empty starter pane. A live
// checklist — start workspace → connect a git provider → connect an agent → create
// the first session — that ticks itself off as the user completes each step and
// disappears once they have a session (or hit "あとで", remembered in localStorage).
// Rendered only on the active empty pane (see TerminalView) so it shows just once.
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSettingsUI } from "../settings/store.ts";
import type { ConnectionsStatus } from "../../types/session.ts";

const DISMISS_KEY = "af.onboarding.dismissed";

export function OnboardingCard() {
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const sessions = useSessionsStore((s) => s.sessions);
  const openNewSession = useSessionsStore((s) => s.openNewSession);
  const openSettings = useSettingsUI((s) => s.openSettings);
  const connKey = useSettingsUI((s) => s.connTick);
  const [conns, setConns] = useState<ConnectionsStatus | null>(null); // null = still probing
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem(DISMISS_KEY) === "1";
    } catch {
      return false;
    }
  });

  // Connections aren't kept in global state — fetch here, refetch when a connect
  // action bumps connTick. On failure assume "not connected" so the card still helps.
  useEffect(() => {
    let alive = true;
    api("api/connections")
      .then((d) => alive && setConns(d && !d.error ? d : {}))
      .catch(() => alive && setConns({}));
    return () => {
      alive = false;
    };
  }, [connKey]);

  // Show until the user has a session (then they're past first-run), or dismisses.
  // We keep showing even after git/agent are connected so the final step — create
  // the first session — stays guided while there are zero sessions.
  if (dismissed || sessions.length > 0) return null;
  if (conns === null) return null; // wait for the probe so checks don't flash wrong

  const running = wsState === "running";
  const startBusy = wsStartBusy(wsState); // start already in flight / ECS cold pull
  const gitOk = !!(conns.github?.connected || conns.bitbucket?.connected);
  const agentOk = !!(
    conns.claude?.connected ||
    conns.codex?.connected ||
    (conns.opencode?.envs?.length ?? 0) > 0
  );

  const dismiss = () => {
    try {
      localStorage.setItem(DISMISS_KEY, "1");
    } catch {}
    setDismissed(true);
  };

  const steps = [
    {
      done: running,
      label: "ワークスペースを起動",
      hint: "コンテナを起動して作業を開始します",
      // Power glyph (⏻) to match the WS バー's start/stop toggle — same metaphor.
      // Inert while a start is already under way (same guard as the WS バー) so
      // mashing the CTA can't fire concurrent start POSTs.
      cta: running
        ? null
        : { text: startBusy ? "起動中…" : "起動", glyph: "⏻", disabled: startBusy, on: () => void startWs() },
    },
    {
      done: gitOk,
      label: "git プロバイダを接続",
      hint: "GitHub / Bitbucket からリポジトリを clone できます",
      cta: gitOk ? null : { text: "接続する", icon: "plug", on: () => openSettings("git") },
    },
    {
      done: agentOk,
      label: "エージェントを接続",
      hint: "Claude / Codex / opencode にサインインします",
      cta: agentOk ? null : { text: "接続する", icon: "plug", on: () => openSettings("agents") },
    },
    {
      done: false,
      label: "最初のセッションを作成",
      hint: "shell や claude などを起動します",
      cta: { text: "新規セッション", icon: "add", on: openNewSession },
    },
  ];
  const nextIdx = steps.findIndex((s) => !s.done); // the step to highlight

  return (
    <div className="onboard">
      <div className="onboard-card">
        <div className="onboard-title">Agent Fleet へようこそ</div>
        <div className="onboard-sub">数ステップで使い始められます</div>
        <ol className="onboard-steps">
          {steps.map((s, i) => (
            <li key={i} className={"onboard-step" + (s.done ? " done" : "") + (i === nextIdx ? " next" : "")}>
              <span className="onboard-mark">
                <Icon name={s.done ? "pass-filled" : i === nextIdx ? "arrow-right" : "circle-large-outline"} />
              </span>
              <span className="onboard-body">
                <span className="onboard-label">{s.label}</span>
                <span className="onboard-hint">{s.hint}</span>
              </span>
              {s.cta &&
                (() => {
                  const cta = s.cta as { text: string; on: () => void; icon?: string; glyph?: string; disabled?: boolean };
                  return (
                    <button
                      className={"onboard-cta" + (i === nextIdx ? " primary" : "")}
                      disabled={cta.disabled}
                      onClick={cta.on}
                    >
                      {cta.glyph ? (
                        <span className="onboard-cta-glyph" aria-hidden="true">
                          {cta.glyph}
                        </span>
                      ) : (
                        <Icon name={cta.icon!} />
                      )}{" "}
                      {cta.text}
                    </button>
                  );
                })()}
            </li>
          ))}
        </ol>
        <div className="onboard-foot">
          <button className="ghost" onClick={dismiss}>
            あとで
          </button>
        </div>
      </div>
    </div>
  );
}
