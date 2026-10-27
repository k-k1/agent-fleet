// RepoSkillsModal — a repository's skills per agent kind (#2795), entered from the repo row's
// right-click menu. The companion of Settings > Skills (#2792): that one is what you set up for
// yourself, this one is what the working copy gives an agent — committed, shared with the team.
// Read-only. The Agent scans the same project roots the composer's skill picker does
// (GET api/skills/catalog?repo=&subdir=), so a Subdir launch shows what it would really see:
// a nearer copy of a name shadows one above it, and a folder a CLI reads but the repository has
// not created is listed with the path to create.
import { useCallback, useMemo, useState } from "react";
import { api, errText, isTransientErr } from "../../core/api/client.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { useT } from "../../lib/i18n/index.ts";
import { agentOf } from "../../agents/registry.ts";
import { Modal } from "../../ui/Modal.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { useSettingsUI } from "../settings/store.ts";
import { SkillDetail } from "../settings/personal/SkillsTab.tsx";
import type { CatalogKind, CatalogRoot, CatalogSkill } from "../settings/personal/SkillsTab.tsx";

// project = inside the working copy; user = a folder ABOVE it that the CLI also walks.
const SCOPES = ["project", "user"] as const;

/** The file a missing folder would hold first, for the "create" hint. */
const exampleFile = (r: CatalogRoot) => `${r.rel}/${r.form === "commands" ? "<name>.md" : "<name>/SKILL.md"}`;

export function RepoSkillsModal({ repo, onClose }: { repo: string; onClose?: () => void }) {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";
  const [kinds, setKinds] = useState<CatalogKind[] | null>(null);
  const [err, setErr] = useState("");
  // `draft` is what is typed; `subdir` is what was asked for, so the list never reloads per key.
  const [draft, setDraft] = useState("");
  const [subdir, setSubdir] = useState("");
  const [kind, setKind] = useState("claude");
  const [selected, setSelected] = useState<CatalogSkill | null>(null);

  const load = useCallback(
    async (signal: AbortSignal) => {
      if (!running) return true;
      const r = await api("api/skills/catalog?repo=" + encodeURIComponent(repo) + "&subdir=" + encodeURIComponent(subdir));
      if (signal.aborted) return true;
      if (isTransientErr(r)) return false;
      if (r?.error || !Array.isArray(r?.kinds)) {
        setKinds(null);
        setErr(r?.error?.code === "no_subdir" ? tr("repo_skills.no_subdir", { subdir }) : r?.error ? errText(r.error) : tr("skills.load_failed"));
        return true;
      }
      setErr("");
      setKinds(r.kinds as CatalogKind[]);
      return true;
    },
    [repo, subdir, running, tr],
  );
  useRetryLoad(load, [running, repo, subdir]);

  const source = useMemo(() => ({ repo, subdir }), [repo, subdir]);
  const current = kinds?.find((k) => k.kind === kind) ?? kinds?.[0];
  const apply = () => {
    setSubdir(draft.trim());
    setSelected(null);
  };

  return (
    <Modal title={tr("repo_skills.title", { repo })} onClose={onClose} className="repo-skills-modal">
      <div className="ui-modal-body skills-tab">
        {!running ? (
          <EmptyState icon="book" title={tr("instr.ws_required_title")} hint={tr("skills.ws_required_hint")}>
            <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
              {wsStartBusy(wsState) ? tr("common.starting") : tr("instr.start_ws")}
            </Button>
          </EmptyState>
        ) : (
          <>
            <p className="ds-hint">{tr("repo_skills.intro")}</p>
            <form
              className="skills-subdir"
              onSubmit={(e) => {
                e.preventDefault();
                apply();
              }}
            >
              <label>
                {tr("repo_skills.subdir")}
                <input
                  type="text"
                  value={draft}
                  placeholder={tr("repo_skills.subdir_ph")}
                  onChange={(e) => setDraft(e.target.value)}
                  spellCheck={false}
                />
              </label>
              <Button type="submit" variant="ghost" disabled={draft.trim() === subdir}>
                {tr("repo_skills.show")}
              </Button>
            </form>
            {err && <div className="form-err">{err}</div>}
            {!kinds && !err && <p className="ds-hint">{tr("common.loading")}</p>}
            {kinds && current && (
              <>
                <div className="skills-kinds" role="tablist" aria-label={tr("skills.kind_nav")}>
                  {kinds.map((k) => (
                    <button
                      key={k.kind}
                      type="button"
                      role="tab"
                      aria-selected={k.kind === current.kind}
                      className={"skills-kind" + (k.kind === current.kind ? " active" : "")}
                      onClick={() => {
                        setKind(k.kind);
                        setSelected(null);
                      }}
                    >
                      {agentOf(k.kind).label} <span className="skills-count">{k.skills.length}</span>
                    </button>
                  ))}
                </div>
                <KindPanel kind={current} source={source} selected={selected} onSelect={setSelected} onClose={onClose} />
              </>
            )}
          </>
        )}
      </div>
    </Modal>
  );
}

function KindPanel({
  kind,
  source,
  selected,
  onSelect,
  onClose,
}: {
  kind: CatalogKind;
  source: { repo: string; subdir: string };
  selected: CatalogSkill | null;
  onSelect: (s: CatalogSkill | null) => void;
  onClose?: () => void;
}) {
  const tr = useT();
  const openSettings = useSettingsUI((s) => s.openSettings);
  return (
    <div id="repo-skills-panel" role="tabpanel">
      <h4 className="ds-title">{tr("skills.roots_head")}</h4>
      {kind.roots.length === 0 ? (
        <p className="ds-hint">{tr("repo_skills.no_roots")}</p>
      ) : (
        <ul className="skills-roots">
          {kind.roots.map((r) => (
            <li key={r.dir} className={r.exists ? "" : "skills-missing"}>
              <code title={r.dir}>{r.rel}</code>
              <span className="skills-badge">{tr(r.form === "commands" ? "skills.form_commands" : "skills.form_skills")}</span>
              {r.scope === "user" && <span className="skills-badge">{tr("repo_skills.scope_user")}</span>}
              {!r.exists && (
                <span className="skills-badge" title={exampleFile(r)}>
                  {tr("repo_skills.not_present")}
                </span>
              )}
              {!r.exists && <span className="ds-hint">{tr("repo_skills.create", { path: exampleFile(r) })}</span>}
            </li>
          ))}
        </ul>
      )}
      {kind.skills.length === 0 && kind.roots.length > 0 && <p className="ds-hint">{tr("repo_skills.none")}</p>}
      {SCOPES.map((scope) => {
        const list = kind.skills.filter((s) => s.scope === scope);
        if (list.length === 0) return null;
        return (
          <section key={scope} className="skills-group">
            <h4 className="ds-title">
              {tr(`repo_skills.group_${scope}`)} <span className="skills-count">{list.length}</span>
            </h4>
            <ul className="skills-list">
              {list.map((s) => {
                const key = s.rel || s.name;
                const active = !!selected && (selected.rel || selected.name) === key;
                return (
                  <li key={key}>
                    <button
                      type="button"
                      className={"skills-row" + (active ? " active" : "") + (s.shadowedBy ? " skills-shadowed" : "")}
                      aria-expanded={active}
                      onClick={() => onSelect(active ? null : s)}
                    >
                      <span className="skills-name">{s.name}</span>
                      {s.type === "command" && <span className="skills-badge">{tr("skills.form_commands")}</span>}
                      {s.shadowedBy && <span className="skills-badge">{tr("skills.shadowed")}</span>}
                      {s.outside && <span className="skills-badge">{tr("repo_skills.outside_badge")}</span>}
                      {!s.userInvocable && <span className="skills-badge">{tr("skills.not_invocable")}</span>}
                      {s.description && <span className="skills-desc">{s.description}</span>}
                    </button>
                    {active && (
                      <>
                        {s.shadowedBy && <p className="ds-hint">{tr("skills.shadowed_by", { by: relOf(kind, s.shadowedBy) })}</p>}
                        {s.alsoPersonal && (
                          <p className="ds-hint">
                            {tr("repo_skills.also_personal")}{" "}
                            <Button
                              variant="ghost"
                              small
                              onClick={() => {
                                onClose?.();
                                openSettings("skills:" + kind.kind);
                              }}
                            >
                              {tr("repo_skills.open_personal")}
                            </Button>
                          </p>
                        )}
                        <SkillDetail skill={{ ...s, shadowedBy: undefined }} source={source} />
                      </>
                    )}
                  </li>
                );
              })}
            </ul>
          </section>
        );
      })}
    </div>
  );
}

/** The working-copy-relative spelling of a shadowing entry's absolute path. */
const relOf = (kind: CatalogKind, abs: string) => kind.skills.find((x) => x.path === abs)?.rel ?? abs;
