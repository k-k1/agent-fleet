// The launch modal's first-prompt template picker (#1469): a searchable popover over the
// person's own templates (synced ui-prefs, lib/launchTemplates.ts), the repository's
// `.agent-fleet/launch-prompts.md` entries and the recent-prompt history (lib/promptHistory.ts).
//
// Keyboard follows the skill picker (SkillList): focus stays in one input and ↑/↓ move a
// highlighted row, Enter takes it, Esc closes. The input is an ARIA combobox pointing at the
// highlighted option, so a screen reader follows the highlight without focus moving. Row
// actions (edit, delete, save as template) sit in the preview pane rather than inside the
// options: an interactive control nested in role="option" is unreachable for assistive tech.
//
// The repository's .claude commands and skills are not offered here any more: the skill picker
// beside this one invokes them for every kind, while pasting their body only ever worked for
// claude and duplicated the list.
import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, RefObject } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import { useSettings } from "../../lib/settings.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useEscLayer } from "../../lib/escLayer.ts";
import { coarsePointer } from "../../lib/device.ts";
import {
  TEMPLATE_NAME_MAX,
  deleteTemplate,
  normalizeTemplates,
  promptExcerpt,
  promptTitle,
  saveTemplate,
  scopeRepo,
  templatesFor,
} from "../../lib/launchTemplates.ts";
import type { LaunchTemplate, TemplateDraft } from "../../lib/launchTemplates.ts";
import { deletePromptHistory, migrateLegacyPromptHistory, readPromptHistory } from "../../lib/promptHistory.ts";
import type { PromptTemplateItem } from "./api.ts";

interface Entry {
  key: string;
  group: "user" | "file" | "history";
  title: string;
  excerpt: string;
  body: string;
  tpl?: LaunchTemplate;
}

type Mode =
  | { t: "list" }
  | { t: "edit"; draft: TemplateDraft; error?: string }
  | { t: "confirm"; text: string }
  | { t: "delete"; id: string };

const GROUP_LABEL: Record<Entry["group"], MsgKey> = {
  user: "launch.tmpl.group_user",
  file: "launch.tmpl.group_file",
  history: "launch.history",
};

export function PromptTemplateButton({
  btnRef,
  open,
  disabled,
  onToggle,
}: {
  btnRef: RefObject<HTMLButtonElement | null>;
  open: boolean;
  disabled?: boolean;
  onToggle: () => void;
}) {
  const tr = useT();
  return (
    <button
      type="button"
      ref={btnRef}
      className={"ghost launch-tmpl-btn" + (open ? " on" : "")}
      title={tr("launch.template_insert_title")}
      aria-haspopup="dialog"
      aria-expanded={open}
      disabled={disabled}
      onClick={onToggle}
    >
      <Icon name="notebook-template" /> {tr("launch.tmpl.button")}
    </button>
  );
}

export function PromptTemplatePopover({
  repo,
  fileItems,
  expand,
  hasText,
  btnRef,
  onInsert,
  onClose,
}: {
  repo: string;
  /** The repository's `.agent-fleet/launch-prompts.md` entries. */
  fileItems: PromptTemplateItem[];
  /** {{repo}}/{{branch}}/{{path}} expansion for this launch. */
  expand: (body: string) => string;
  /** The first prompt already holds text: picking asks before touching it. */
  hasText: boolean;
  btnRef: RefObject<HTMLButtonElement | null>;
  onInsert: (text: string, mode: "replace" | "cursor") => void;
  onClose: () => void;
}) {
  const tr = useT();
  const settings = useSettings();
  const popRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listId = useId();
  const [query, setQuery] = useState("");
  const [activeKey, setActiveKey] = useState("");
  const [mode, setMode] = useState<Mode>({ t: "list" });
  // History can also live in device-only localStorage, which no settings change announces.
  const [histRev, setHistRev] = useState(0);
  const coarse = coarsePointer();

  // The device-only history of older Consoles moves into the synced list here, once the server
  // copy has been read (lib/promptHistory.ts says why not earlier).
  useEffect(() => {
    migrateLegacyPromptHistory();
  }, [settings]);

  const base = scopeRepo(repo);
  const entries = useMemo<Entry[]>(() => {
    const own = templatesFor(normalizeTemplates(settings.launchTemplates), repo).map((t) => ({
      key: "u:" + t.id,
      group: "user" as const,
      title: t.name,
      excerpt: promptExcerpt(t.body, false),
      body: t.body,
      tpl: t,
    }));
    const files = fileItems.map((it) => ({
      key: "f:" + it.id,
      group: "file" as const,
      title: it.label,
      excerpt: promptExcerpt(it.body, false),
      body: it.body,
    }));
    const hist = readPromptHistory(repo).map((h) => ({
      key: "h:" + h,
      group: "history" as const,
      title: promptTitle(h),
      excerpt: promptExcerpt(h),
      body: h,
    }));
    return [...own, ...files, ...hist];
    // histRev: re-read the device-only history after a delete.
  }, [settings.launchTemplates, settings.launchHistory, fileItems, repo, histRev]);

  const q = query.trim().toLowerCase();
  const shown = q ? entries.filter((e) => (e.title + "\n" + e.body).toLowerCase().includes(q)) : entries;
  const activeIdx = Math.max(0, shown.findIndex((e) => e.key === activeKey));
  const active = shown[activeIdx];

  useDismiss([popRef, btnRef], true, onClose);
  // Esc inside the editor, the overwrite question or a delete confirmation backs out to the list
  // instead of closing everything (this layer joins above useDismiss's).
  useEscLayer(() => setMode({ t: "list" }), mode.t !== "list");

  useEffect(() => {
    if (mode.t === "list" && !coarse) searchRef.current?.focus({ preventScroll: true });
  }, [mode.t, coarse]);
  // The panel opens in the dialog's scrolling body and may start below its fold.
  useEffect(() => {
    popRef.current?.scrollIntoView?.({ block: "nearest" });
  }, []);
  // Closing removes the focused search box; hand focus back to the button that opened the panel
  // unless an insert has already put it in the prompt. Cleanup runs after the panel left the DOM.
  useEffect(() => {
    const btn = btnRef.current;
    return () => {
      const el = document.activeElement;
      if (!coarsePointer() && (!el || el === document.body)) btn?.focus({ preventScroll: true });
    };
  }, [btnRef]);
  // Block body on purpose: scrollIntoView may return a Promise, which an arrow expression would
  // hand to React as the effect's cleanup.
  useEffect(() => {
    document.getElementById(optId(listId, activeIdx))?.scrollIntoView?.({ block: "nearest" });
  }, [activeIdx, listId]);

  const choose = (e: Entry) => {
    const text = expand(e.body);
    if (hasText) {
      setMode({ t: "confirm", text });
      return;
    }
    onInsert(text, "replace");
    onClose();
  };
  const finish = (how: "replace" | "cursor") => {
    if (mode.t !== "confirm") return;
    onInsert(mode.text, how);
    onClose();
  };

  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return;
    if ((e.key === "ArrowDown" || e.key === "ArrowUp") && shown.length) {
      e.preventDefault();
      const n = shown.length;
      setActiveKey(shown[(activeIdx + (e.key === "ArrowDown" ? 1 : n - 1)) % n].key);
      return;
    }
    if (e.key === "Enter" && !e.ctrlKey && !e.metaKey && !e.shiftKey) {
      e.preventDefault();
      if (active) choose(active);
    }
  };

  const startEdit = (draft: TemplateDraft) => setMode({ t: "edit", draft });
  const save = () => {
    if (mode.t !== "edit") return;
    const r = saveTemplate(mode.draft);
    if (!r.ok) {
      setMode({ ...mode, error: tr(("launch.tmpl.err." + r.error) as MsgKey) });
      return;
    }
    setQuery("");
    setActiveKey("u:" + r.id);
    setMode({ t: "list" });
  };

  return (
    <div className="launch-tmpl-pop" ref={popRef} role="dialog" aria-label={tr("launch.tmpl.title")}>
      {mode.t === "edit" ? (
        <TemplateEditor
          draft={mode.draft}
          error={mode.error}
          repo={base}
          onChange={(draft) => setMode({ t: "edit", draft })}
          onSave={save}
          onCancel={() => setMode({ t: "list" })}
        />
      ) : mode.t === "confirm" ? (
        <div className="launch-tmpl-ask" role="group" aria-label={tr("launch.tmpl.confirm_title")}>
          <p>{tr("launch.tmpl.confirm_text")}</p>
          <div className="launch-tmpl-ask-btns">
            <Button variant="primary" small autoFocus onClick={() => finish("cursor")}>
              {tr("launch.tmpl.insert_cursor")}
            </Button>
            <Button small onClick={() => finish("replace")}>
              {tr("launch.tmpl.replace")}
            </Button>
            <Button variant="ghost" small onClick={() => setMode({ t: "list" })}>
              {tr("common.cancel")}
            </Button>
          </div>
        </div>
      ) : (
        <>
          <div className="launch-tmpl-search">
            <Icon name="search" />
            <input
              ref={searchRef}
              type="search"
              role="combobox"
              aria-expanded="true"
              aria-controls={listId}
              aria-autocomplete="list"
              aria-activedescendant={active ? optId(listId, activeIdx) : undefined}
              aria-label={tr("launch.tmpl.search")}
              placeholder={tr("launch.tmpl.search")}
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setActiveKey("");
              }}
              onKeyDown={onSearchKey}
            />
          </div>
          <div className="launch-tmpl-body">
            <div className="launch-tmpl-list" id={listId} role="listbox" aria-label={tr("launch.tmpl.title")}>
              {shown.length === 0 ? (
                <div className="launch-tmpl-note">{tr(q ? "launch.tmpl.no_match" : "launch.tmpl.empty")}</div>
              ) : (
                shown.map((e, i) => (
                  <div key={e.key} role="presentation">
                    {(i === 0 || shown[i - 1].group !== e.group) && (
                      <div className="launch-tmpl-group" role="presentation">
                        {tr(GROUP_LABEL[e.group])}
                      </div>
                    )}
                    <div
                      id={optId(listId, i)}
                      role="option"
                      aria-selected={i === activeIdx}
                      className={"launch-tmpl-item" + (i === activeIdx ? " sel" : "")}
                      onMouseMove={() => e.key !== active?.key && setActiveKey(e.key)}
                      onMouseDown={(ev) => ev.preventDefault()}
                      // A touch has no hover to preview with, so the first tap previews and the
                      // preview's Insert button commits.
                      onClick={() => (coarse && e.key !== active?.key ? setActiveKey(e.key) : choose(e))}
                    >
                      <span className="launch-tmpl-item-title">
                        {e.title || tr("launch.tmpl.untitled")}
                        {e.tpl?.repo ? <span className="launch-tmpl-scope">{e.tpl.repo}</span> : null}
                      </span>
                      {e.excerpt ? <span className="launch-tmpl-item-excerpt">{e.excerpt}</span> : null}
                    </div>
                  </div>
                ))
              )}
            </div>
            {active && (
              <div className="launch-tmpl-preview" aria-label={tr("launch.tmpl.preview")} role="region">
                <pre className="launch-tmpl-preview-text">{expand(active.body)}</pre>
                {mode.t === "delete" && active.tpl?.id === mode.id ? (
                  <div className="launch-tmpl-actions" role="group">
                    <span>{tr("launch.tmpl.delete_confirm")}</span>
                    <Button
                      variant="danger"
                      small
                      onClick={() => {
                        deleteTemplate(mode.id);
                        setMode({ t: "list" });
                      }}
                    >
                      {tr("launch.tmpl.delete")}
                    </Button>
                    <Button variant="ghost" small onClick={() => setMode({ t: "list" })}>
                      {tr("common.cancel")}
                    </Button>
                  </div>
                ) : (
                  <div className="launch-tmpl-actions">
                    <Button variant="primary" small onClick={() => choose(active)}>
                      {tr("launch.tmpl.insert")}
                    </Button>
                    {active.tpl && (
                      <>
                        <Button
                          small
                          icon="edit"
                          onClick={() =>
                            startEdit({ id: active.tpl!.id, name: active.tpl!.name, body: active.tpl!.body, repo: active.tpl!.repo })
                          }
                        >
                          {tr("launch.tmpl.edit")}
                        </Button>
                        <Button small icon="trash" onClick={() => setMode({ t: "delete", id: active.tpl!.id })}>
                          {tr("launch.tmpl.delete")}
                        </Button>
                      </>
                    )}
                    {active.group === "history" && (
                      <>
                        <Button
                          small
                          icon="save"
                          onClick={() => startEdit({ name: promptTitle(active.body).slice(0, TEMPLATE_NAME_MAX), body: active.body, repo: "" })}
                        >
                          {tr("launch.tmpl.save_history")}
                        </Button>
                        <Button
                          small
                          icon="trash"
                          onClick={() => {
                            deletePromptHistory(repo, active.body);
                            setHistRev((n) => n + 1);
                          }}
                        >
                          {tr("launch.tmpl.delete")}
                        </Button>
                      </>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
          <div className="launch-tmpl-foot">
            <Button variant="ghost" small icon="add" onClick={() => startEdit({ name: "", body: "", repo: "" })}>
              {tr("launch.tmpl.new")}
            </Button>
            {!coarse && <span className="launch-tmpl-keys">{tr("launch.tmpl.keys")}</span>}
          </div>
        </>
      )}
    </div>
  );
}

const optId = (listId: string, i: number) => `${listId}-opt-${i}`;

function TemplateEditor({
  draft,
  error,
  repo,
  onChange,
  onSave,
  onCancel,
}: {
  draft: TemplateDraft;
  error?: string;
  /** Base repository the "this repository only" scope would name. */
  repo: string;
  onChange: (d: TemplateDraft) => void;
  onSave: () => void;
  onCancel: () => void;
}) {
  const tr = useT();
  const nameId = useId();
  const bodyId = useId();
  const errId = useId();
  return (
    <div className="launch-tmpl-edit" role="group" aria-label={tr(draft.id ? "launch.tmpl.edit_title" : "launch.tmpl.new_title")}>
      <div className="launch-tmpl-edit-head">{tr(draft.id ? "launch.tmpl.edit_title" : "launch.tmpl.new_title")}</div>
      <label htmlFor={nameId}>{tr("launch.tmpl.name")}</label>
      <input
        id={nameId}
        type="text"
        autoFocus={!coarsePointer()}
        maxLength={TEMPLATE_NAME_MAX}
        value={draft.name}
        aria-invalid={!!error}
        aria-describedby={error ? errId : undefined}
        onChange={(e) => onChange({ ...draft, name: e.target.value })}
      />
      <label htmlFor={bodyId}>{tr("launch.tmpl.body")}</label>
      <textarea
        id={bodyId}
        rows={6}
        value={draft.body}
        aria-describedby={error ? errId : undefined}
        onChange={(e) => onChange({ ...draft, body: e.target.value })}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.ctrlKey || e.metaKey) && !e.nativeEvent.isComposing) {
            e.preventDefault();
            onSave();
          }
        }}
      />
      <span className="launch-tmpl-hint">{tr("launch.tmpl.vars_hint")}</span>
      <fieldset className="launch-tmpl-scope-set">
        <legend>{tr("launch.tmpl.scope")}</legend>
        <label>
          <input type="radio" name={nameId + "-scope"} checked={!draft.repo} onChange={() => onChange({ ...draft, repo: "" })} />
          {tr("launch.tmpl.scope_all")}
        </label>
        <label>
          <input type="radio" name={nameId + "-scope"} checked={draft.repo === repo} onChange={() => onChange({ ...draft, repo })} />
          {tr("launch.tmpl.scope_repo", { repo })}
        </label>
      </fieldset>
      {error && (
        <div className="launch-tmpl-err" id={errId} role="alert">
          {error}
        </div>
      )}
      <div className="launch-tmpl-ask-btns">
        <Button variant="primary" small onClick={onSave}>
          {tr("launch.tmpl.save")}
        </Button>
        <Button variant="ghost" small onClick={onCancel}>
          {tr("common.cancel")}
        </Button>
      </div>
    </div>
  );
}
