// SkillsTab — Settings > Skills (#2792): each agent kind's personal skills, where each one
// lives and what it says. Read-only. The list comes from the Agent (GET api/skills/catalog),
// which scans the same roots the CLIs read; the body comes from api/skills/file, which serves
// only files inside those roots — the Files viewer cannot open them, because the credentials
// sit in the folders above. Repository skills are a separate view (#2795).
//
// The view keeps what the session's skill picker drops on purpose: entries another root
// shadows, `user-invocable: false` ones and Agent Fleet's own `af-*` skills, because "why is my
// skill not used" is exactly what someone opens this screen to answer.
import { useCallback, useState } from "react";
import { api, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { agentOf } from "../../../agents/registry.ts";
import { copyText } from "../../../lib/clipboard.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { MarkdownView } from "../../viewer/MarkdownView.tsx";
import { ViewNav, useSectionView } from "../memory/viewNav.tsx";

export interface CatalogRoot {
  dir: string;
  scope: string;
  form: string;
  exists: boolean;
}
export interface CatalogSkill {
  name: string;
  description?: string;
  argumentHint?: string;
  type: string;
  scope: string;
  root?: string;
  path?: string;
  userInvocable: boolean;
  shadowedBy?: string;
  files?: string[];
}
export interface CatalogKind {
  kind: string;
  roots: CatalogRoot[];
  skills: CatalogSkill[];
}

export const SKILLS_SECTION = "skills";
// Display order of the scope groups: the member's own first, then what Agent Fleet and the CLI
// bring along.
const SCOPES = ["user", "fleet", "cli"] as const;
// The kinds the Agent reports, fixed so the remembered tab can be restored before the list has
// loaded. One the Agent does not report simply has no tab.
const KINDS = ["claude", "codex", "opencode", "muse"];

const dirOf = (p: string) => p.slice(0, p.lastIndexOf("/"));

export function SkillsTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";
  const [kinds, setKinds] = useState<CatalogKind[] | null>(null);
  const [err, setErr] = useState("");

  const load = useCallback(
    async (signal: AbortSignal) => {
      if (!running) return true;
      const r = await api("api/skills/catalog");
      if (signal.aborted) return true;
      if (isTransientErr(r)) return false;
      if (r?.error || !Array.isArray(r?.kinds)) {
        setErr(r?.error ? errText(r.error) : tr("skills.load_failed"));
        return true;
      }
      setErr("");
      setKinds(r.kinds as CatalogKind[]);
      return true;
    },
    [running, tr],
  );
  useRetryLoad(load, [running]);

  const [kind, setKind] = useSectionView(SKILLS_SECTION, KINDS, KINDS[0]);
  const [selected, setSelected] = useState<CatalogSkill | null>(null);
  const current = kinds?.find((k) => k.kind === kind) ?? kinds?.[0];

  if (!running) {
    return (
      <div className="skills-tab">
        <EmptyState icon="book" title={tr("instr.ws_required_title")} hint={tr("skills.ws_required_hint")}>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("instr.start_ws")}
          </Button>
        </EmptyState>
      </div>
    );
  }

  return (
    <div className="skills-tab">
      <p className="ds-hint">{tr("skills.intro")}</p>
      {err && <div className="form-err">{err}</div>}
      {!kinds && !err && <p className="ds-hint">{tr("common.loading")}</p>}
      {kinds && current && (
        <>
          <ViewNav
            label={tr("skills.kind_nav")}
            prefix="skills"
            views={kinds.map((k) => ({ key: k.kind, label: agentOf(k.kind).label, count: k.skills.length }))}
            value={current.kind}
            onChange={(k) => {
              setKind(k);
              setSelected(null);
            }}
          />
          <div id="skills-panel" role="tabpanel">
            <KindPanel kind={current} selected={selected} onSelect={setSelected} />
          </div>
          <p className="ds-hint">{tr("skills.other_kinds")}</p>
        </>
      )}
    </div>
  );
}

function KindPanel({
  kind,
  selected,
  onSelect,
}: {
  kind: CatalogKind;
  selected: CatalogSkill | null;
  onSelect: (s: CatalogSkill | null) => void;
}) {
  const tr = useT();
  return (
    <>
      <h4 className="ds-title">{tr("skills.roots_head")}</h4>
      <ul className="skills-roots">
        {kind.roots.map((r) => (
          <li key={r.dir} className={r.exists ? "" : "skills-missing"}>
            <code title={r.dir}>{r.dir}</code>
            <span className="skills-badge">{tr(r.form === "commands" ? "skills.form_commands" : "skills.form_skills")}</span>
            {r.scope === "cli" && <span className="skills-badge">{tr("skills.scope_cli")}</span>}
            {!r.exists && <span className="skills-badge">{tr("skills.root_missing")}</span>}
          </li>
        ))}
      </ul>
      {kind.skills.length === 0 && <p className="ds-hint">{tr("skills.none")}</p>}
      {SCOPES.map((scope) => {
        const list = kind.skills.filter((s) => s.scope === scope);
        if (list.length === 0) return null;
        return (
          <section key={scope} className="skills-group">
            <h4 className="ds-title">
              {tr(`skills.scope_${scope}`)} <span className="skills-count">{list.length}</span>
            </h4>
            {scope === "fleet" && <p className="ds-hint">{tr("skills.fleet_hint")}</p>}
            <ul className="skills-list">
              {list.map((s) => {
                const key = s.path || s.name;
                const active = selected && (selected.path || selected.name) === key;
                return (
                  <li key={key}>
                    <button
                      type="button"
                      className={"skills-row" + (active ? " active" : "") + (s.shadowedBy ? " skills-shadowed" : "")}
                      aria-expanded={!!active}
                      onClick={() => onSelect(active ? null : s)}
                    >
                      <span className="skills-name">{s.name}</span>
                      {s.type === "command" && <span className="skills-badge">{tr("skills.form_commands")}</span>}
                      {s.shadowedBy && <span className="skills-badge">{tr("skills.shadowed")}</span>}
                      {!s.userInvocable && <span className="skills-badge">{tr("skills.not_invocable")}</span>}
                      {s.description && <span className="skills-desc">{s.description}</span>}
                    </button>
                    {active && <SkillDetail skill={s} />}
                  </li>
                );
              })}
            </ul>
          </section>
        );
      })}
    </>
  );
}

function SkillDetail({ skill }: { skill: CatalogSkill }) {
  const tr = useT();
  const toast = useToast();
  // The file showing: the skill's own SKILL.md (or command file) first, then whichever of its
  // folder's files was picked.
  const [file, setFile] = useState<{ path: string; content: string } | null>(null);
  const [err, setErr] = useState("");
  const folder = skill.type === "skill" && skill.path ? dirOf(skill.path) : "";

  const open = useCallback(async (path: string, signal?: AbortSignal) => {
    const r = await api("api/skills/file?path=" + encodeURIComponent(path));
    if (signal?.aborted) return true;
    if (isTransientErr(r)) return false;
    if (r?.error || typeof r?.content !== "string") {
      setErr(r?.error ? errText(r.error) : tr("skills.load_failed"));
      return true;
    }
    setErr("");
    setFile({ path, content: r.content });
    return true;
  }, [tr]);
  const loadMain = useCallback(
    async (signal: AbortSignal) => (skill.path ? open(skill.path, signal) : true),
    [skill.path, open],
  );
  useRetryLoad(loadMain, [skill.path]);

  const copy = async (p: string) => toast(tr((await copyText(p)) ? "skills.path_copied" : "skills.copy_failed"));

  return (
    <div className="skills-detail">
      {skill.path ? (
        <div className="skills-where">
          <code title={skill.path}>{skill.path}</code>
          <Button variant="ghost" icon="copy" onClick={() => void copy(skill.path!)}>
            {tr("skills.copy_path")}
          </Button>
        </div>
      ) : (
        <p className="ds-hint">{tr("skills.bundled_hint")}</p>
      )}
      {skill.shadowedBy && <p className="ds-hint">{tr("skills.shadowed_by", { by: skill.shadowedBy })}</p>}
      {skill.argumentHint && (
        <p className="ds-hint">
          {tr("skills.argument_hint")} <code>{skill.argumentHint}</code>
        </p>
      )}
      {folder && (skill.files?.length ?? 0) > 0 && (
        <div className="skills-files">
          <span>{tr("skills.files")}</span>
          <Button variant="ghost" onClick={() => void open(skill.path!)} disabled={file?.path === skill.path}>
            SKILL.md
          </Button>
          {skill.files!.map((f) => {
            const p = folder + "/" + f;
            return (
              <Button key={f} variant="ghost" onClick={() => void open(p)} disabled={file?.path === p}>
                {f}
              </Button>
            );
          })}
        </div>
      )}
      {err && <div className="form-err">{err}</div>}
      {file &&
        (file.path.endsWith(".md") ? (
          <div className="skills-body">
            <MarkdownView source={file.content} />
          </div>
        ) : (
          <pre className="instr-peek-body skills-body">{file.content}</pre>
        ))}
    </div>
  );
}
