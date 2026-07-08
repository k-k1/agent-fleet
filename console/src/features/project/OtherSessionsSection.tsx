// OtherSessionsSection — the catch-all for sessions that belong to no working copy
// (a shell in home, or a session whose repo was removed). Since the project tree
// replaced the single Sessions section, this always-present section also carries
// the global session-header actions (整理 / アーカイブ / 新規).
import { Section } from "../../ui/Section.tsx";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionUI } from "../sessions/ui.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { SessionRow } from "../sessions/SessionRow.tsx";
import { useReposStore } from "../repos/store.ts";
import { useRepoRailContext } from "../repos/useRepoRail.ts";
import { orphanSessions } from "../../lib/project.ts";

export function OtherSessionsSection() {
  const sessions = useSessionsStore((s) => s.sessions);
  const openNewSession = useSessionsStore((s) => s.openNewSession);
  const openArchived = useSessionUI((u) => u.openArchived);
  const repos = useReposStore((s) => s.repos);
  const ctx = useRepoRailContext();
  const actions = useSessionActions();
  const orphans = orphanSessions(sessions, repos);

  return (
    <Section
      id="other-sessions"
      title="その他のセッション"
      icon="terminal"
      count={orphans.length}
      actions={
        <>
          <Button
            small
            variant="ghost"
            icon="clear-all"
            title="停止中をまとめてアーカイブ（shell/ssm は削除）"
            disabled={!sessions.some((s) => !s.alive)}
            onClick={actions.clearStopped}
          >
            整理
          </Button>
          <Button small variant="ghost" icon="archive" title="アーカイブを開く（復帰）" onClick={openArchived}>
            アーカイブ
          </Button>
          <Button
            small
            variant="ghost"
            icon="add"
            title={ctx.running ? "新規セッション" : "新規セッション（ワークスペース停止中）"}
            disabled={!ctx.running}
            onClick={openNewSession}
          >
            新規
          </Button>
        </>
      }
    >
      <ul className="sess-list">
        {orphans.length === 0 ? (
          <EmptyState icon="comment-discussion" title="リポジトリ外のセッションはありません" hint="repos/ 配下以外で動くセッション（shell など）がここに並びます" />
        ) : (
          orphans.map((s) => (
            <SessionRow
              key={s.name}
              s={s}
              selected={ctx.activeSession === s.name}
              opens={ctx.sPanes?.get(s.name) || []}
              multi={ctx.multiPane}
              running={ctx.running}
              actions={actions}
            />
          ))
        )}
      </ul>
    </Section>
  );
}
