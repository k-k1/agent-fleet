// Personal first-prompt templates for the launch modal (#1469). They live in the synced ui-prefs
// (Settings.launchTemplates) so they follow the user to every browser and device. The whole
// ui-prefs object is one PUT capped at 64 KiB by the Agent, and a PUT over it fails the sync of
// EVERY setting, so this module caps the templates on write (a refused save says why) and on read
// (a value imported or written by another client is trimmed rather than trusted).
import { getSettings, setSettings } from "./settings.ts";
import { folderBase } from "./workingSets.ts";

export interface LaunchTemplate {
  id: string;
  name: string;
  body: string;
  /** Base repository the template is limited to; "" = every repository. */
  repo: string;
  /** Last edit, epoch ms. */
  at: number;
}

export const TEMPLATE_MAX_COUNT = 40;
export const TEMPLATE_NAME_MAX = 80;
export const TEMPLATE_BODY_MAX_BYTES = 8 * 1024;
export const TEMPLATES_MAX_BYTES = 16 * 1024;

export type TemplateError = "name_required" | "body_required" | "name_too_long" | "body_too_long" | "too_many" | "full";

const enc = new TextEncoder();
export const byteLength = (s: string): number => enc.encode(s).length;

/** The repository a template or history entry is scoped by: a worktree counts as its base clone,
 *  so a template limited to "app" also shows in "app@feature-x". */
export const scopeRepo = (repo: string): string => folderBase(repo || "");

function isTemplate(v: unknown): v is LaunchTemplate {
  if (!v || typeof v !== "object") return false;
  const t = v as Record<string, unknown>;
  return typeof t.id === "string" && !!t.id && typeof t.name === "string" && typeof t.body === "string" && typeof t.repo === "string";
}

/** Valid entries only, unique ids, within every cap — the oldest edits are what a cap drops. */
export function normalizeTemplates(v: unknown): LaunchTemplate[] {
  if (!Array.isArray(v)) return [];
  const seen = new Set<string>();
  const out: LaunchTemplate[] = [];
  for (const x of v) {
    if (!isTemplate(x) || seen.has(x.id)) continue;
    if (!x.name.trim() || !x.body.trim()) continue;
    if (x.name.length > TEMPLATE_NAME_MAX || byteLength(x.body) > TEMPLATE_BODY_MAX_BYTES) continue;
    seen.add(x.id);
    out.push({ id: x.id, name: x.name, body: x.body, repo: x.repo, at: typeof x.at === "number" ? x.at : 0 });
  }
  if (out.length <= TEMPLATE_MAX_COUNT && byteLength(JSON.stringify(out)) <= TEMPLATES_MAX_BYTES) return out;
  const byAge = [...out].sort((a, b) => b.at - a.at);
  while (byAge.length > TEMPLATE_MAX_COUNT || byteLength(JSON.stringify(byAge)) > TEMPLATES_MAX_BYTES) byAge.pop();
  const keep = new Set(byAge.map((t) => t.id));
  return out.filter((t) => keep.has(t.id));
}

export const readTemplates = (): LaunchTemplate[] => normalizeTemplates(getSettings().launchTemplates);

/** The templates shown when launching in `repo`: the all-repository ones plus that repository's. */
export function templatesFor(all: LaunchTemplate[], repo: string): LaunchTemplate[] {
  const base = scopeRepo(repo);
  return all.filter((t) => !t.repo || t.repo === base);
}

export interface TemplateDraft {
  /** Absent = a new template. */
  id?: string;
  name: string;
  body: string;
  repo: string;
}

/** Validates without saving, against the list the save would produce. */
export function checkTemplate(d: TemplateDraft, all: LaunchTemplate[] = readTemplates()): TemplateError | null {
  const name = d.name.trim();
  if (!name) return "name_required";
  if (!d.body.trim()) return "body_required";
  if (name.length > TEMPLATE_NAME_MAX) return "name_too_long";
  if (byteLength(d.body) > TEMPLATE_BODY_MAX_BYTES) return "body_too_long";
  const others = all.filter((t) => t.id !== d.id);
  if (!d.id && others.length >= TEMPLATE_MAX_COUNT) return "too_many";
  const next = [...others, { id: d.id || "x", name, body: d.body, repo: d.repo, at: 0 }];
  if (byteLength(JSON.stringify(next)) > TEMPLATES_MAX_BYTES) return "full";
  return null;
}

const newId = (): string => {
  try {
    return crypto.randomUUID();
  } catch {
    return "t" + Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
  }
};

/** Create or update; an existing template keeps its place in the list. */
export function saveTemplate(d: TemplateDraft): { ok: true; id: string } | { ok: false; error: TemplateError } {
  const all = readTemplates();
  const error = checkTemplate(d, all);
  if (error) return { ok: false, error };
  const id = d.id && all.some((t) => t.id === d.id) ? d.id : newId();
  const tpl: LaunchTemplate = { id, name: d.name.trim(), body: d.body, repo: scopeRepo(d.repo), at: Date.now() };
  const next = all.some((t) => t.id === id) ? all.map((t) => (t.id === id ? tpl : t)) : [...all, tpl];
  setSettings({ launchTemplates: next });
  return { ok: true, id };
}

export function deleteTemplate(id: string): void {
  const all = readTemplates();
  if (!all.some((t) => t.id === id)) return;
  setSettings({ launchTemplates: all.filter((t) => t.id !== id) });
}

// promptTitle / promptExcerpt: the one-line summary a long prompt is shown by. The title is the
// first non-blank line (a markdown heading's #s dropped); the excerpt is what follows, folded
// onto one line so CSS can clamp it.
export function promptTitle(body: string): string {
  const line = body.split("\n").find((l) => l.trim()) || "";
  return line.trim().replace(/^#+\s*/, "");
}

export function promptExcerpt(body: string, skipFirstLine = true): string {
  const lines = body.split("\n");
  const first = lines.findIndex((l) => l.trim());
  const rest = skipFirstLine && first >= 0 ? lines.slice(first + 1) : lines;
  return rest.join(" ").replace(/\s+/g, " ").trim().slice(0, 240);
}
