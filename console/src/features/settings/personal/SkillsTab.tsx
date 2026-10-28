// SkillsTab — Settings > Skills (#2792): each agent kind's personal skills, where each one
// lives and what it says. Read-only. The list comes from the Agent (GET api/skills/catalog),
// which scans the same roots the CLIs read; the body comes from api/skills/file, which serves
// only files inside those roots — the Files viewer cannot open them, because the credentials
// sit in the folders above. Repository skills are a separate view (#2795).
//
// The view keeps what the session's skill picker drops on purpose: entries another root
// shadows, `user-invocable: false` ones and Agent Fleet's own `af-*` skills, because "why is my
// skill not used" is exactly what someone opens this screen to answer.
import { useCallback, useState, type MouseEvent } from "react";
import { api, errText, isTransientErr } from "../../../core/api/client.ts";
import { useRetryLoad } from "../../../lib/retryLoad.ts";
import { useWorkspaceStore, wsStartBusy } from "../../../core/store/workspace.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { EmptyState } from "../../../ui/EmptyState.tsx";
import { agentOf, skillsCatalogKinds } from "../../../agents/registry.ts";
import { copyText } from "../../../lib/clipboard.ts";
import { isExternalUrl } from "../../../lib/filemeta.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { MarkdownView } from "../../viewer/MarkdownView.tsx";
import { openFileMode } from "../../viewer/openFile.ts";
import { ViewNav, useSectionView } from "../memory/viewNav.tsx";

export interface CatalogRoot {
  dir: string;
  scope: string;
  form: string;
  exists: boolean;
  /** Repository view only: the folder relative to the working copy ("../" above it). */
  rel?: string;
}
export interface CatalogSkill {
  name: string;
  /** opencode's frontmatter name when it differs from the path-derived ID in `name`. */
  title?: string;
  description?: string;
  argumentHint?: string;
  type: string;
  scope: string;
  root?: string;
  path?: string;
  userInvocable: boolean;
  shadowedBy?: string;
  files?: string[];
  /** Repository view only: `path` relative to the working copy, which is what its file route takes. */
  rel?: string;
  /** Repository view only: the file resolves outside the working copy, so it is named, not read. */
  outside?: boolean;
  /** Repository view only: the same kind has a personal skill of this name. */
  alsoPersonal?: boolean;
}

/** Where a skill's files are read from: the personal routes, or one repository (+ Subdir). */
export interface SkillSource {
  repo: string;
  subdir: string;
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
// The kinds with a skills tab (caps.skillsCatalog), known before the Agent's list has loaded so
// the remembered tab can be restored. One the Agent does not report simply has no tab.

const dirOf = (p: string) => p.slice(0, p.lastIndexOf("/"));

/** The file a relative link in a skill's Markdown points at, resolved against the directory of
 *  the file showing; null for anything that is not a file reference (URLs, in-page anchors). */
export function resolveSkillLink(href: string, fromFile: string): string | null {
  if (!href || href.startsWith("#") || isExternalUrl(href)) return null;
  let ref = href.split("#")[0].split("?")[0];
  try {
    ref = decodeURIComponent(ref);
  } catch {
    /* keep the raw spelling */
  }
  if (!ref) return null;
  // A working-copy-relative file (repository view) stays relative and keeps a leading "..": a
  // skill in a folder above the working copy is addressed as "../.claude/skills/…".
  const relative = !fromFile.startsWith("/");
  if (relative && ref.startsWith("/")) return null;
  const parts = (ref.startsWith("/") ? ref : dirOf(fromFile) + "/" + ref).split("/");
  const out: string[] = [];
  for (const seg of parts) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") {
      if (relative && (out.length === 0 || out[out.length - 1] === "..")) out.push("..");
      else out.pop();
    } else out.push(seg);
  }
  return relative ? out.join("/") : "/" + out.join("/");
}

/** The route one file of a skill is read from. */
export function skillFileUrl(path: string, src?: SkillSource): string {
  return src
    ? "api/skills/file?repo=" + encodeURIComponent(src.repo) + "&subdir=" + encodeURIComponent(src.subdir) + "&path=" + encodeURIComponent(path)
    : "api/skills/file?path=" + encodeURIComponent(path);
}

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

  const [kind, setKind] = useSectionView(SKILLS_SECTION, skillsCatalogKinds, skillsCatalogKinds[0]);
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
                      {s.title && <span className="skills-title">{s.title}</span>}
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

export function SkillDetail({ skill, source }: { skill: CatalogSkill; source?: SkillSource }) {
  const tr = useT();
  const toast = useToast();
  // The id the file route takes: absolute for a personal skill, working-copy-relative for a
  // repository one.
  const loc = (source ? skill.rel : skill.path) ?? "";
  // One selected path drives the fetch, so a slower answer for an earlier pick can never land
  // over a later one: useRetryLoad aborts the previous request whenever `want` changes.
  const [want, setWant] = useState(loc);
  const [file, setFile] = useState<{ path: string; content: string } | null>(null);
  const [err, setErr] = useState("");
  const folder = skill.type === "skill" && loc ? dirOf(loc) : "";

  const load = useCallback(
    async (signal: AbortSignal) => {
      if (!want || skill.outside) return true;
      const r = await api(skillFileUrl(want, source));
      if (signal.aborted) return true;
      if (isTransientErr(r)) return false;
      if (r?.error || typeof r?.content !== "string") {
        setErr(r?.error ? errText(r.error) : tr("skills.load_failed"));
        return true;
      }
      setErr("");
      setFile({ path: want, content: r.content });
      return true;
    },
    [want, source, skill.outside, tr],
  );
  useRetryLoad(load, [want]);

  // Relative links in a skill point at its own files, which the Files viewer cannot open (the
  // folders are outside the browse root). Taken in the capture phase, before MarkdownView's own
  // link handler, and opened here through the skills file route instead.
  // A middle click arrives as auxclick, which MarkdownView also handles, so both are taken.
  const onLinkClick = (e: MouseEvent<HTMLDivElement>) => {
    if (e.type === "auxclick" && e.button !== 1) return;
    const a = (e.target as HTMLElement).closest?.("a");
    if (!a || !file) return;
    const target = resolveSkillLink(a.getAttribute("href") ?? "", file.path);
    if (!target) return;
    e.preventDefault();
    e.stopPropagation();
    setWant(target);
  };

  const copy = async (p: string) => toast(tr((await copyText(p)) ? "skills.path_copied" : "skills.copy_failed"));

  return (
    <div className="skills-detail">
      {skill.outside ? (
        <p className="ds-hint">{tr("repo_skills.outside")}</p>
      ) : loc ? (
        <div className="skills-where">
          <code title={skill.path}>{loc}</code>
          <Button variant="ghost" icon="copy" onClick={() => void copy(loc)}>
            {tr("skills.copy_path")}
          </Button>
          {source && !loc.startsWith("../") && (
            <Button variant="ghost" icon="go-to-file" onClick={() => openFileMode(`repos/${source.repo}/${want}`, "view")}>
              {tr("repo_skills.open_in_viewer")}
            </Button>
          )}
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
          <Button variant="ghost" onClick={() => setWant(loc)} disabled={want === loc}>
            SKILL.md
          </Button>
          {skill.files!.map((f) => {
            const p = folder + "/" + f;
            return (
              <Button key={f} variant="ghost" onClick={() => setWant(p)} disabled={want === p}>
                {f}
              </Button>
            );
          })}
        </div>
      )}
      {err && <div className="form-err">{err}</div>}
      {file &&
        (file.path.endsWith(".md") ? (
          <div className="skills-body" onClickCapture={onLinkClick} onAuxClickCapture={onLinkClick}>
            <MarkdownView source={file.content} />
          </div>
        ) : (
          <pre className="instr-peek-body skills-body">{file.content}</pre>
        ))}
    </div>
  );
}
